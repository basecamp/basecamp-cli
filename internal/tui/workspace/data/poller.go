package data

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// PollMsg is sent when a poll interval fires. Tag identifies which poller triggered.
type PollMsg struct {
	Tag string
}

// PollConfig configures a single polling channel.
type PollConfig struct {
	Tag        string
	Base       time.Duration // interval when focused and active
	Background time.Duration // interval when blurred
	Max        time.Duration // maximum backoff
	misses     int           // consecutive polls with no new data
	current    time.Duration
	focused    bool
}

// Poller manages adaptive polling for multiple data channels.
type Poller struct {
	channels map[string]*PollConfig
}

// NewPoller creates an empty poller.
func NewPoller() *Poller {
	return &Poller{
		channels: make(map[string]*PollConfig),
	}
}

// Add registers a polling channel.
func (p *Poller) Add(cfg PollConfig) {
	cfg.current = cfg.Base
	cfg.focused = true
	p.channels[cfg.Tag] = &cfg
}

// Start returns tea.Cmds to begin all registered polls.
func (p *Poller) Start() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(p.channels))
	for _, ch := range p.channels {
		cmds = append(cmds, p.tick(ch))
	}
	return tea.Batch(cmds...)
}

// Schedule returns a tea.Cmd for the next tick of the given channel.
func (p *Poller) Schedule(tag string) tea.Cmd {
	ch, ok := p.channels[tag]
	if !ok {
		return nil
	}
	return p.tick(ch)
}

// RecordHit resets the interval for a channel (new data arrived).
func (p *Poller) RecordHit(tag string) {
	ch, ok := p.channels[tag]
	if !ok {
		return
	}
	ch.misses = 0
	if ch.focused {
		ch.current = ch.Base
	} else {
		ch.current = ch.Background
	}
}

// RecordMiss increases the interval (no new data).
func (p *Poller) RecordMiss(tag string) {
	ch, ok := p.channels[tag]
	if !ok {
		return
	}
	ch.misses++
	ch.current *= 2
	if ch.current > ch.Max {
		ch.current = ch.Max
	}
}

// SetFocused adjusts base interval for focus/blur.
func (p *Poller) SetFocused(tag string, focused bool) {
	ch, ok := p.channels[tag]
	if !ok {
		return
	}
	ch.focused = focused
	if focused {
		ch.current = ch.Base
	} else if ch.current < ch.Background {
		ch.current = ch.Background
	}
}

func (p *Poller) tick(ch *PollConfig) tea.Cmd {
	tag := ch.Tag
	interval := ch.current
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return PollMsg{Tag: tag}
	})
}
