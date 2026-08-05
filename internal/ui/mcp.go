package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/get-vix/vix/internal/daemon"
	"github.com/get-vix/vix/internal/protocol"
)

// mcpTabDocURL is the documentation anchor advertised in the MCP tab header.
const mcpTabDocURL = "https://getvix.dev/docs#mcp"

// mcpListMsg carries the configured MCP servers shown in the MCP tab.
type mcpListMsg struct {
	servers []protocol.MCPServerSummary
}

// fetchMCPServers lists the configured MCP servers. Triggered on entering the
// MCP tab and on event.mcp_changed broadcasts.
func fetchMCPServers(socketPath, authToken string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		servers, err := client.ListMCPServers()
		if err != nil {
			return mcpListMsg{}
		}
		return mcpListMsg{servers: servers}
	}
}

// setMCPEnabled toggles an MCP server's enabled flag, then refreshes the list.
func setMCPEnabled(socketPath, authToken, name string, enabled bool) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		client.SetMCPEnabled(name, enabled)
		return fetchMCPServers(socketPath, authToken)()
	}
}

// authorizeMCP starts the OAuth flow for a server: it asks the daemon for the
// authorization URL, opens the browser, then refreshes the list. The exchange
// completes asynchronously in the daemon, which broadcasts event.mcp_changed to
// refresh the tab once the token is stored.
func authorizeMCP(socketPath, authToken, name string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		if url, err := client.AuthorizeMCP(name); err == nil && url != "" {
			openLoginBrowser(url)
		}
		return fetchMCPServers(socketPath, authToken)()
	}
}

// logoutMCP deletes the stored OAuth token for a server, then refreshes the list.
func logoutMCP(socketPath, authToken, name string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		client.LogoutMCP(name)
		return fetchMCPServers(socketPath, authToken)()
	}
}

// mcpStatusLabel renders a server's status in the "Status" column, prefixing a ⚠
// marker for connection failures so a broken server stands out and surfacing the
// OAuth "needs auth" / "signed in" states.
func mcpStatusLabel(sum protocol.MCPServerSummary) string {
	switch sum.Status {
	case "connected":
		return "connected"
	case "disabled":
		return "disabled"
	case "needs_auth":
		return "🔒 needs auth"
	default:
		return "⚠ error"
	}
}

// renderMCPView renders the MCP tab: a short header followed by a table of
// configured MCP servers with an enable/disable checkbox, transport type,
// status, and tool count. selectedRow indexes the servers.
func renderMCPView(servers []protocol.MCPServerSummary, width, height int, s Styles, selectedRow int) string {
	innerWidth := width - 4
	if innerWidth < 0 {
		innerWidth = 0
	}

	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	hintStyle := lipgloss.NewStyle().Italic(true).Foreground(colorSecondary)
	linkStyle := lipgloss.NewStyle().Foreground(colorPrimary)

	var rows []string
	rows = append(rows,
		"",
		"  "+descStyle.Render("Here is the list of all the MCP servers configured in your home settings."),
		"  "+descStyle.Render("Press space to enable/disable the selected server."),
		"  "+descStyle.Render("For OAuth servers: press a to authenticate, o to sign out."),
		"  "+descStyle.Render("Docs: ")+linkStyle.Render(mcpTabDocURL),
		"",
		"  "+hintStyle.Render("Tip: MCP servers are defined under \"mcp_servers\" in ~/.vix/settings.json."),
		"",
	)

	// Column widths: [box] Name  Type  Status  Tools. Name flexes.
	const colBox = 3
	const colType = 8
	const colStatus = 14
	const colTools = 7
	colName := innerWidth - colBox - colType - colStatus - colTools - 10
	if colName < 12 {
		colName = 12
	}

	header := fmt.Sprintf("    %-*s  %-*s  %-*s  %-*s",
		colName, "Name", colType, "Type", colStatus, "Status", colTools, "Tools")
	headerRule := "  " + threadHeaderRuleStyle.Render(strings.Repeat("─", min(colBox+colName+colType+colStatus+colTools+8, innerWidth)))

	rows = append(rows,
		"  "+threadGroupHeaderStyle.Render("MCP Servers"),
		"",
		threadColumnHeaderStyle.Render(header),
		headerRule,
	)

	if len(servers) == 0 {
		rows = append(rows, "", "  "+descStyle.Italic(true).Render("No MCP servers configured."))
		content := strings.Join(rows, "\n")
		return s.ViewportFocusedStyle.Width(width).Height(height).Render(content)
	}

	for i, srv := range servers {
		tools := "—"
		if srv.Enabled && srv.Status == "connected" {
			tools = fmt.Sprintf("%d", srv.ToolCount)
		}
		plain := enabledBox(srv.Enabled) + " " +
			jobsCell(srv.Name, colName) + "  " +
			jobsCell(srv.Type, colType) + "  " +
			jobsCell(mcpStatusLabel(srv), colStatus) + "  " +
			jobsCell(tools, colTools)
		if i == selectedRow {
			rows = append(rows, threadRowSelectedStyle.Render("  ")+threadRowSelectedStyle.Render(plain))
		} else {
			rows = append(rows, "  "+plain)
		}
	}

	content := strings.Join(rows, "\n")
	return s.ViewportFocusedStyle.Width(width).Height(height).Render(content)
}
