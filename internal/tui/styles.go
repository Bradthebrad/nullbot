package tui

import "github.com/charmbracelet/lipgloss"

var (
	headerStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#081113")).
			Foreground(lipgloss.Color("#9DD9D2")).
			Bold(true)

	blockShadowStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFD300")).
				Background(lipgloss.Color("#081113")).
				Bold(true)

	bannerFillStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFD300")).
			Background(lipgloss.Color("#081113")).
			Bold(true)

	bannerFaceHighStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFD300")).
				Background(lipgloss.Color("#081113")).
				Bold(true)

	bannerFaceLowStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#C97816")).
				Background(lipgloss.Color("#081113")).
				Bold(true)

	bannerOutlineStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#B86B1C")).
				Background(lipgloss.Color("#081113")).
				Bold(true)

	bannerShadowStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#B86B1C")).
				Background(lipgloss.Color("#081113")).
				Bold(true)

	labelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#D9B44A")).
			Bold(true).
			PaddingLeft(1)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.ThickBorder()).
			BorderForeground(lipgloss.Color("#2E7D78")).
			Foreground(lipgloss.Color("#E8F1F2")).
			Background(lipgloss.Color("#10191C"))

	activityStyle = lipgloss.NewStyle().
			Border(lipgloss.ThickBorder()).
			BorderForeground(lipgloss.Color("#D9B44A")).
			Foreground(lipgloss.Color("#EDE6D6")).
			Background(lipgloss.Color("#171D21"))

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#102A2A")).
			Background(lipgloss.Color("#9DD9D2")).
			Bold(true)

	statusWorkStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9DD9D2")).
			Background(lipgloss.Color("#102A2A")).
			Bold(true)

	spinnerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFD300")).
			Background(lipgloss.Color("#102A2A")).
			Bold(true)

	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("#92B4A7")).
			Background(lipgloss.Color("#0D1518")).
			Padding(0, 1)

	selectedInputStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#081113")).
				Background(lipgloss.Color("#F4D35E")).
				Bold(true)

	selectedRowStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#F4F7F5")).
				Background(lipgloss.Color("#24383B")).
				Bold(true)

	buttonStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#081113")).
			Background(lipgloss.Color("#9DD9D2")).
			Bold(true).
			Padding(0, 1)

	modalStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#F4D35E")).
			Foreground(lipgloss.Color("#F4F7F5")).
			Background(lipgloss.Color("#142226")).
			Padding(1, 2)

	modalTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#F4D35E")).
			MarginBottom(1)

	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#92B4A7")).
			MarginTop(1)

	userStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#F4D35E"))

	botStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#9DD9D2"))

	headingStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFD300"))

	bulletStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F4F7F5"))

	quoteStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#92B4A7")).
			Italic(true)

	codeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#D9E6A3")).
			Background(lipgloss.Color("#1D2A2D"))

	mutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#92A0A6"))
)
