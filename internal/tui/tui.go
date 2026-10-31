package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"omenight/internal/api"
)

var (
	styTitle   = lipgloss.NewStyle().Bold(true)
	styDim     = lipgloss.NewStyle().Faint(true)
	styHead    = lipgloss.NewStyle().Bold(true).Faint(true)
	styOnline  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styPending = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	styRevoked = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styErr     = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

type tickMsg time.Time

type snapshotMsg struct {
	snap *api.Snapshot
	err  error
}

type Model struct {
	socket string
	snap   *api.Snapshot
	err    error
}

func New(socket string) Model { return Model{socket: socket} }

func (m Model) Init() tea.Cmd { return fetch(m.socket) }

func fetch(socket string) tea.Cmd {
	return func() tea.Msg {
		s, err := api.Fetch(socket)
		return snapshotMsg{snap: s, err: err}
	}
}

func wait() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "r":
			return m, fetch(m.socket)
		}
	case snapshotMsg:
		m.snap, m.err = msg.snap, msg.err
		return m, wait()
	case tickMsg:
		return m, fetch(m.socket)
	}
	return m, nil
}

func (m Model) View() string {
	var b strings.Builder

	if m.snap == nil {
		b.WriteString(styDim.Render("connecting to "+m.socket) + "\n")
		if m.err != nil {
			b.WriteString(styErr.Render(m.err.Error()) + "\n")
		}
		b.WriteString("\n" + styDim.Render("q quit"))
		return b.String()
	}

	s := m.snap
	up := time.Since(s.StartedAt).Round(time.Second)
	b.WriteString(styTitle.Render(" omenight relay ") +
		styDim.Render(s.Listen) + "   " + styDim.Render("up "+up.String()) + "\n")
	b.WriteString(styDim.Render(" host key "+s.HostKey) + "\n\n")

	if m.err != nil {
		b.WriteString(styErr.Render(" "+m.err.Error()) + "\n")
	}

	if len(s.Clients) == 0 {
		b.WriteString(styDim.Render(" no clients enrolled yet") + "\n")
	} else {
		row := "%-14s %-8s %-6s %5s %8s %8s  %-22s %s"
		b.WriteString(styHead.Render(fmt.Sprintf(row,
			"NAME", "STATUS", "ONLINE", "CONNS", "UP", "DOWN", "ALLOW", "LAST SEEN")) + "\n")

		for _, c := range s.Clients {
			online := "no"
			if c.Online {
				online = "yes"
			}
			allow := strings.Join(c.Allow, ",")
			if allow == "" {
				allow = "-"
			}
			seen := "-"
			if c.Online {
				seen = "now"
			} else if !c.LastSeen.IsZero() {
				seen = time.Since(c.LastSeen).Round(time.Second).String() + " ago"
			}

			line := fmt.Sprintf(row,
				trunc(c.Name, 14), c.Status, online,
				fmt.Sprint(c.Conns), human(c.Up), human(c.Down),
				trunc(allow, 22), seen)
			b.WriteString(statusColor(c.Status).Render(line) + "\n")
		}
	}

	b.WriteString("\n" + styDim.Render(" refreshing every 1s · r now · q quit"))
	return b.String()
}

func statusColor(status string) lipgloss.Style {
	switch status {
	case "active":
		return styOnline
	case "pending":
		return styPending
	case "revoked":
		return styRevoked
	}
	return styDim
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}
