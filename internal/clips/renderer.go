package clips

// Renderer performs FFmpeg clip work without any store dependency.
type Renderer struct {
	cfg Config
}

// NewRenderer builds a standalone FFmpeg renderer with normalized settings.
func NewRenderer(cfg Config) *Renderer {
	return &Renderer{cfg: normalizeConfig(cfg)}
}

// CacheKey returns the stable render identity, including its encoding profile.
func (s *Renderer) CacheKey(r Request) string {
	mode := r.Mode
	switch mode {
	case "preview":
		mode += "|" + s.cfg.PreviewRes + "|" + s.cfg.CPUPreset
	case "accurate":
		mode += "|" + s.cfg.AccurateRes + "|" + s.cfg.CPUPreset
	}
	return CacheKey(r.MediaID, r.Start, r.End, mode, r.PadMs)
}
