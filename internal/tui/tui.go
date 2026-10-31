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

type doneMsg struct {
	resp *api.Response
	err  error
}

type Model struct {
	socket string
	snap   *api.Snapshot
	err    error
	cursor int
	flash  string
}

func New(socket string) Model { return Model{socket: socket} }

func (m Model) Init() tea.Cmd { return fetch(m.socket) }

func fetch(socket string) tea.Cmd {
	return func() tea.Msg {
		s, err := api.Fetch(socket)
		return snapshotMsg{snap: s, err: err}
	}
}

func doAction(socket string, cmd api.Command) tea.Cmd {
	return func() tea.Msg {
		r, err := api.Do(socket, cmd)
		return doneMsg{resp: r, err: err}
	}
}

func wait() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) selected() string {
	if m.snap == nil || m.cursor < 0 || m.cursor >= len(m.snap.Clients) {
		return ""
	}
	return m.snap.Clients[m.cursor].Name
}

func (m *Model) clamp() {
	if m.snap == nil || len(m.snap.Clients) == 0 {
		m.cursor = 0
		return
	}
	if m.cursor >= len(m.snap.Clients) {
		m.cursor = len(m.snap.Clients) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "r":
			return m, fetch(m.socket)
		case "j", "down":
			m.cursor++
			m.clamp()
		case "k", "up":
			m.cursor--
			m.clamp()
		case "a":
			if name := m.selected(); name != "" {
				m.flash = "approving " + name + "…"
				return m, doAction(m.socket, api.Command{Cmd: "approve", Name: name})
			}
		case "x":
			if name := m.selected(); name != "" {
				m.flash = "revoking " + name + "…"
				return m, doAction(m.socket, api.Command{Cmd: "revoke", Name: name})
			}
		}
	case snapshotMsg:
		m.snap, m.err = msg.snap, msg.err
		m.clamp()
		return m, wait()
	case doneMsg:
		switch {
		case msg.err != nil:
			m.flash = msg.err.Error()
		case msg.resp == nil:
			m.flash = "no response from daemon"
		case msg.resp.Snapshot != nil:
			m.snap = msg.resp.Snapshot
			m.clamp()
			m.flash = msg.resp.Message
		default:
			m.flash = "daemon does not support commands (upgrade it)"
		}
		return m, fetch(m.socket)
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
		b.WriteString(styDim.Render(" no clients enrolled yet — waiting for a node to connect") + "\n")
	} else {
		row := "%s%-14s %-8s %-6s %5s %8s %8s  %-22s %s"
		b.WriteString(styHead.Render(fmt.Sprintf(row,
			"  ", "NAME", "STATUS", "ONLINE", "CONNS", "UP", "DOWN", "ALLOW", "LAST SEEN")) + "\n")

		for i, c := range s.Clients {
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

			marker := "  "
			if i == m.cursor {
				marker = "› "
			}
			line := fmt.Sprintf(row, marker,
				trunc(c.Name, 14), c.Status, online,
				fmt.Sprint(c.Conns), human(c.Up), human(c.Down),
				trunc(allow, 22), seen)

			style := statusColor(c.Status)
			if i == m.cursor {
				style = style.Bold(true)
			}
			b.WriteString(style.Render(line) + "\n")
		}
	}

	if m.flash != "" {
		b.WriteString("\n " + m.flash)
	}
	b.WriteString("\n" + styDim.Render(" j/k move · a approve · x revoke · r refresh · q quit"))
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
