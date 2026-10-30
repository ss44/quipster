package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Node is one registered worker node of the cluster. Nodes register against
// the main server, stay pending until an operator approves them, and then use
// their token to claim jobs over HTTP. Credentials are stored in plain text,
// matching the existing API_TOKEN posture: a database leak exposes node
// tokens (job-processing rights only, never admin rights), and revocation
// clears them.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Node struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Version     string `json:"version"`
	Secret      string `json:"-"`
	Token       string `json:"-"`
	Fingerprint string `json:"-"`
	LastSeen    int64  `json:"lastSeen"`
	JobsDone    int64  `json:"jobsDone"`
	JobsFailed  int64  `json:"jobsFailed"`
	CreatedAt   int64  `json:"createdAt"`
	ID          int64  `json:"id"`
}

// Node status values.
const (
	NodePending  = "pending"
	NodeApproved = "approved"
	NodeRevoked  = "revoked"
)

// workerPrefix marks job rows claimed through the distributed node protocol.
const workerPrefix = "node-"

// NodeWorkerID is the jobs.worker_id value used for claims made by the given
// node id. Job ownership checks compare against this exact string.
func NodeWorkerID(nodeID int64) string {
	return workerPrefix + strconv.FormatInt(nodeID, 10)
}

// NodeIDFromWorker extracts the node id from a worker id, or 0 when the
// worker id is not a node claim. Callers group running jobs per node for the
// admin dashboard with it.
func NodeIDFromWorker(workerID string) int64 {
	if !strings.HasPrefix(workerID, workerPrefix) {
		return 0
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(workerID, workerPrefix), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// UpsertNode registers a node by fingerprint. A known fingerprint re-registers
// as pending with a fresh secret and a cleared token: the operator must
// re-approve before the node can claim work again, so a lost client state file
// cannot silently resurrect access.
func (s *Store) UpsertNode(name, fingerprint, version, secret string) (*Node, error) {
	now := nowMillis()
	if _, err := s.db.Exec(`INSERT INTO nodes (name, fingerprint, secret, status, version, last_seen, created_at)
		VALUES (?,?,?,'pending',?,?,?)
		ON CONFLICT(fingerprint) DO UPDATE SET
			name=excluded.name, secret=excluded.secret, version=excluded.version,
			token='', status='pending', last_seen=excluded.last_seen`,
		name, fingerprint, secret, version, now, now); err != nil {
		return nil, err
	}
	return s.NodeByFingerprint(fingerprint)
}

// NodeByID loads one node by id, returning nil when absent.
func (s *Store) NodeByID(id int64) (*Node, error) {
	return s.scanNode(s.db.QueryRow(`SELECT `+nodeCols+` FROM nodes WHERE id=?`, id))
}

// NodeByFingerprint loads one node by fingerprint, returning nil when absent.
func (s *Store) NodeByFingerprint(fp string) (*Node, error) {
	return s.scanNode(s.db.QueryRow(`SELECT `+nodeCols+` FROM nodes WHERE fingerprint=?`, fp))
}

// NodeByToken loads the approved node holding the given token, returning nil
// when the token is unknown, stale or revoked.
func (s *Store) NodeByToken(token string) (*Node, error) {
	return s.scanNode(s.db.QueryRow(`SELECT `+nodeCols+` FROM nodes WHERE token=? AND status=?`, token, NodeApproved))
}

// ApproveNode flips a pending node to approved and issues its claim token.
func (s *Store) ApproveNode(id int64, token string) error {
	res, err := s.db.Exec(`UPDATE nodes SET status=?, token=? WHERE id=? AND status=?`,
		NodeApproved, token, id, NodePending)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("node %d is not pending", id)
	}
	return nil
}

// RevokeNode flips an approved node back to revoked and clears its token so
// in-flight claim attempts start failing immediately.
func (s *Store) RevokeNode(id int64) error {
	_, err := s.db.Exec(`UPDATE nodes SET status=?, token='' WHERE id=? AND status=?`, NodeRevoked, id, NodeApproved)
	return err
}

// DeleteNode removes the node row entirely.
func (s *Store) DeleteNode(id int64) error {
	_, err := s.db.Exec(`DELETE FROM nodes WHERE id=?`, id)
	return err
}

// TouchNode records a heartbeat, keeping the node marked live for the
// dashboard and the stale-job janitor.
func (s *Store) TouchNode(id int64) error {
	_, err := s.db.Exec(`UPDATE nodes SET last_seen=? WHERE id=?`, nowMillis(), id)
	return err
}

// BumpNodeCounters adjusts a node's completed/failed job tallies.
func (s *Store) BumpNodeCounters(id int64, failed bool) error {
	if failed {
		_, err := s.db.Exec(`UPDATE nodes SET jobs_failed=jobs_failed+1 WHERE id=?`, id)
		return err
	}
	_, err := s.db.Exec(`UPDATE nodes SET jobs_done=jobs_done+1 WHERE id=?`, id)
	return err
}

// ListNodes returns every registered node, oldest first.
func (s *Store) ListNodes() ([]Node, error) {
	rows, err := s.db.Query(`SELECT ` + nodeCols + ` FROM nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	var out []Node
	for rows.Next() {
		var n Node
		if err := scanFullNode(rows, &n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// CountPendingNodes returns how many nodes currently await approval. The
// register endpoint caps this to bound unauthenticated row growth.
func (s *Store) CountPendingNodes() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE status=?`, NodePending).Scan(&n)
	return n, err
}

// RequeueStaleNodeJobs returns running jobs to the queue when their claiming
// node has gone quiet (missed heartbeats) or no longer exists. The attempt
// spent on the claim is refunded, mirroring RequeueJobSoft, because the work
// itself never reported a failure.
func (s *Store) RequeueStaleNodeJobs(cutoffMillis int64, note string) (int64, error) {
	res, err := s.db.Exec(`UPDATE jobs SET status='queued', worker_id='', error=?,
			attempts = CASE WHEN attempts > 0 THEN attempts - 1 ELSE 0 END
		WHERE status='running' AND worker_id LIKE '`+workerPrefix+`%'
			AND (
				worker_id IN (SELECT '`+workerPrefix+`' || id FROM nodes WHERE last_seen < ?)
				OR worker_id NOT IN (SELECT '`+workerPrefix+`' || id FROM nodes)
			)`, note, cutoffMillis)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RequeueMissingNodeJobs returns running jobs leased to nodeID that the node
// did not include in its latest heartbeat. A node reports every in-flight job
// on each heartbeat, so absence means the lease was orphaned — typically by a
// node restart or a lost claim response. Jobs claimed at or after
// claimedBeforeMillis are skipped so a freshly leased job is not requeued
// before its first heartbeat has had a chance to report it. The claim attempt
// is refunded, mirroring RequeueStaleNodeJobs.
func (s *Store) RequeueMissingNodeJobs(nodeID int64, reported []int64, claimedBeforeMillis int64, note string) (int64, error) {
	args := make([]any, 0, 3+len(reported))
	args = append(args, note, NodeWorkerID(nodeID), claimedBeforeMillis)
	query := `UPDATE jobs SET status='queued', worker_id='', error=?,
			attempts = CASE WHEN attempts > 0 THEN attempts - 1 ELSE 0 END
		WHERE status='running' AND worker_id=? AND started_at < ?`
	if len(reported) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(reported)), ",")
		//nolint:gosec // the concatenated part is a "?" placeholder list built from the reported slice; ids are parameterized.
		query += ` AND id NOT IN (` + ph + `)`
		for _, id := range reported {
			args = append(args, id)
		}
	}
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RequeueNodeJobs returns all running jobs of one node to the queue; used
// when an operator deletes a node so its leases cannot outlive it.
func (s *Store) RequeueNodeJobs(nodeID int64, note string) (int64, error) {
	res, err := s.db.Exec(`UPDATE jobs SET status='queued', worker_id='', error=?,
			attempts = CASE WHEN attempts > 0 THEN attempts - 1 ELSE 0 END
		WHERE status='running' AND worker_id=?`, note, NodeWorkerID(nodeID))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RunningNodeJobs lists running jobs claimed through the node protocol, with
// media metadata attached; the admin dashboard groups them per node.
func (s *Store) RunningNodeJobs() ([]Job, error) {
	rows, err := s.db.Query(`SELECT id, type, payload, status, priority, attempts, worker_id, error, progress, created_at, started_at, finished_at
		FROM jobs WHERE status='running' AND worker_id LIKE '` + workerPrefix + `%' ORDER BY started_at ASC`)
	if err != nil {
		return nil, err
	}
	out, err := scanJobs(rows)
	if err != nil {
		return nil, err
	}
	return s.enrichJobs(out), nil
}

// nodeCols is the column list shared by the node queries.
const nodeCols = `id, name, fingerprint, secret, token, status, version, last_seen, jobs_done, jobs_failed, created_at`

// scanNode scans a single-node query, tolerating sql.ErrNoRows as nil.
func (s *Store) scanNode(row *sql.Row) (*Node, error) {
	var n Node
	err := scanFullNode(row, &n)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// rowScanner abstracts *sql.Row and *sql.Rows for shared node scanning.
type rowScanner interface{ Scan(dest ...any) error }

func scanFullNode(row rowScanner, n *Node) error {
	return row.Scan(&n.ID, &n.Name, &n.Fingerprint, &n.Secret, &n.Token, &n.Status,
		&n.Version, &n.LastSeen, &n.JobsDone, &n.JobsFailed, &n.CreatedAt)
}

// JobOwner reports the status and worker id of a job, for ownership checks
// before a node may report progress or results for it. A missing job yields
// empty strings without error.
func (s *Store) JobOwner(id int64) (status, workerID string, err error) {
	err = s.db.QueryRow(`SELECT status, worker_id FROM jobs WHERE id=?`, id).Scan(&status, &workerID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return status, workerID, err
}
