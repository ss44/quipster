package store

import (
	"reflect"
	"testing"
)

func TestValidExamples(t *testing.T) {
	st := openTest(t)

	if _, err := st.db.Exec(`
		INSERT INTO media_files (id, path, status, title) VALUES 
		(1, '/media/a.mkv', 'indexed', 'A'),
		(2, '/media/b.mkv', 'indexed', 'B'),
		(3, '/media/c.mkv', 'error', 'C'),
		(4, '/media/d.mkv', 'no_subs', 'D');
		
		INSERT INTO chunks (id, media_file_id, start_cue_idx, end_cue_idx, text) VALUES
		(10, 1, 1, 2, 'show me the money'),
		(11, 2, 1, 2, 'show me the money!'),
		(12, 1, 3, 4, 'the city never sleeps'),
		(13, 3, 1, 2, 'run, you fools'),
		(14, 4, 1, 2, 'i could have been a contender'),
		(15, 1, 5, 6, '   ');
	`); err != nil {
		t.Fatal(err)
	}

	candidates := []string{
		"show me the money",
		"the city never sleeps",
		"i could have been a contender",
		"run, you fools",
		"hello world",
	}

	valid, err := st.ValidExamples(candidates)
	if err != nil {
		t.Fatal(err)
	}

	expected := []string{
		"show me the money",     // 2 chunks (id: 10, 11) - most popular
		"the city never sleeps", // 1 chunk (id: 12)
	}

	if !reflect.DeepEqual(valid, expected) {
		t.Errorf("expected %v, got %v", expected, valid)
	}
}
