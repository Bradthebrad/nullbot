package tui

import "github.com/charmbracelet/lipgloss"

type themePalette struct {
	ID          string
	Name        string
	Description string
	Background  string
	Panel       string
	Activity    string
	Modal       string
	Border      string
	ActivityBor string
	Accent      string
	Accent2     string
	BannerTop   string
	Text        string
	Muted       string
	StatusBG    string
	StatusFG    string
	InputBorder string
	CodeBG      string
	CodeFG      string
}

var themes = []themePalette{
	{ID: "steel", Name: "Classic NullBot", Description: "Bold gold NullBot house style: black glass panels, heavy arcade gold borders, amber status bars, and a molten title gradient.", Background: "#050505", Panel: "#10120F", Activity: "#151711", Modal: "#19170D", Border: "#D8A900", ActivityBor: "#FFD33D", Accent: "#FFD300", Accent2: "#E08A12", BannerTop: "#FFF27A", Text: "#F4F0DF", Muted: "#AFA68A", StatusBG: "#F0C23A", StatusFG: "#171103", InputBorder: "#C99A18", CodeBG: "#24200F", CodeFG: "#FFE58A"},
	{ID: "ember", Name: "Ember Terminal", Description: "Warm oranges, smoky panels, and campfire highlights for late-night debugging sessions.", Background: "#140B08", Panel: "#21120D", Activity: "#28170F", Modal: "#2B1810", Border: "#B5531E", ActivityBor: "#F2A65A", Accent: "#FFB000", Accent2: "#FF6B35", BannerTop: "#FFD36A", Text: "#F8E8D8", Muted: "#B99078", StatusBG: "#FFB000", StatusFG: "#241006", InputBorder: "#D9793D", CodeBG: "#351C12", CodeFG: "#FFD7A8"},
	{ID: "aurora", Name: "Aurora Glass", Description: "Polar greens and violet-blue shadows with a glassy northern-lights feel.", Background: "#071014", Panel: "#0D1F25", Activity: "#102830", Modal: "#10242C", Border: "#52E0C4", ActivityBor: "#A78BFA", Accent: "#B6F44A", Accent2: "#52E0C4", BannerTop: "#E8FF8A", Text: "#E7FFF9", Muted: "#8BB8B1", StatusBG: "#52E0C4", StatusFG: "#06201B", InputBorder: "#A78BFA", CodeBG: "#152A33", CodeFG: "#C4FF7A"},
	{ID: "cyberlime", Name: "Cyber Lime", Description: "High-voltage lime, black panels, and sharp hacker-console contrast.", Background: "#050805", Panel: "#0B120B", Activity: "#10180C", Modal: "#0E160E", Border: "#39FF14", ActivityBor: "#F8FF00", Accent: "#F8FF00", Accent2: "#39FF14", BannerTop: "#F9FF6A", Text: "#E9FFE8", Muted: "#82A67D", StatusBG: "#39FF14", StatusFG: "#041004", InputBorder: "#C5FF3D", CodeBG: "#16220F", CodeFG: "#E6FF8F"},
	{ID: "rose", Name: "Rose Circuit", Description: "Soft rose, wine shadows, and mint accents for a warmer assistant personality.", Background: "#130A10", Panel: "#21121C", Activity: "#251722", Modal: "#2B1724", Border: "#FF7AA2", ActivityBor: "#8EF0D0", Accent: "#FFB3C7", Accent2: "#8EF0D0", BannerTop: "#FFD4E2", Text: "#FFF0F5", Muted: "#B98FA0", StatusBG: "#FFB3C7", StatusFG: "#260915", InputBorder: "#FF7AA2", CodeBG: "#351929", CodeFG: "#B9FFE8"},
	{ID: "deepsea", Name: "Deep Sea", Description: "Blue-black ocean panels with cyan foam and submarine instrument vibes.", Background: "#06111F", Panel: "#0B1D33", Activity: "#0E2740", Modal: "#0C2338", Border: "#2FA7FF", ActivityBor: "#3FE6D0", Accent: "#7FDBFF", Accent2: "#3FE6D0", BannerTop: "#D9F7FF", Text: "#E8F6FF", Muted: "#88AFC9", StatusBG: "#7FDBFF", StatusFG: "#06182A", InputBorder: "#2FA7FF", CodeBG: "#102C45", CodeFG: "#B7F6FF"},
	{ID: "mono", Name: "Mono Paper", Description: "Low-distraction grayscale with paper-white text and restrained contrast.", Background: "#0E0E0E", Panel: "#171717", Activity: "#1D1D1D", Modal: "#202020", Border: "#B8B8B8", ActivityBor: "#E6E6E6", Accent: "#FFFFFF", Accent2: "#CFCFCF", BannerTop: "#FFFFFF", Text: "#F2F2F2", Muted: "#9A9A9A", StatusBG: "#E6E6E6", StatusFG: "#111111", InputBorder: "#AFAFAF", CodeBG: "#2A2A2A", CodeFG: "#F5F5F5"},
	{ID: "retro", Name: "Retro Amber", Description: "Classic amber CRT glow for folks who want the terminal to feel like forbidden hardware.", Background: "#100B00", Panel: "#1B1200", Activity: "#211800", Modal: "#221703", Border: "#FFB000", ActivityBor: "#FF7A00", Accent: "#FFD166", Accent2: "#FFB000", BannerTop: "#FFE08A", Text: "#FFECC2", Muted: "#B59252", StatusBG: "#FFB000", StatusFG: "#1B1000", InputBorder: "#D88A00", CodeBG: "#2D1D00", CodeFG: "#FFD166"},
	{ID: "matrix", Name: "Matrix Rain", Description: "Green-on-black with phosphor highlights and old-school terminal intimidation.", Background: "#020604", Panel: "#07100A", Activity: "#09160D", Modal: "#08120B", Border: "#00D26A", ActivityBor: "#7CFF6B", Accent: "#7CFF6B", Accent2: "#00D26A", BannerTop: "#B8FF9A", Text: "#D8FFE1", Muted: "#6AA878", StatusBG: "#00D26A", StatusFG: "#021007", InputBorder: "#3BFF8F", CodeBG: "#0F1D12", CodeFG: "#B6FFB0"},
	{ID: "midnight", Name: "Midnight Violet", Description: "Indigo panels, violet edge light, and moonlit cyan accents.", Background: "#090B1A", Panel: "#11152B", Activity: "#171A35", Modal: "#151832", Border: "#7C83FF", ActivityBor: "#D67CFF", Accent: "#D67CFF", Accent2: "#82D8FF", BannerTop: "#F0C6FF", Text: "#EEF0FF", Muted: "#9EA4C8", StatusBG: "#D67CFF", StatusFG: "#160A22", InputBorder: "#7C83FF", CodeBG: "#1D2242", CodeFG: "#BEE7FF"},
	{ID: "sunset", Name: "Sunset Synth", Description: "Coral, magenta, and warm gold for a synthwave-but-readable layout.", Background: "#160814", Panel: "#24101F", Activity: "#2B1322", Modal: "#2A1124", Border: "#FF5C8A", ActivityBor: "#FFBE5C", Accent: "#FFBE5C", Accent2: "#FF5C8A", BannerTop: "#FFD27A", Text: "#FFF0F7", Muted: "#C18A9F", StatusBG: "#FFBE5C", StatusFG: "#210C14", InputBorder: "#FF7AB6", CodeBG: "#35172A", CodeFG: "#FFD99B"},
	{ID: "forest", Name: "Forest Console", Description: "Moss greens, bark shadows, and soft leaf highlights for a grounded workspace.", Background: "#071008", Panel: "#101A10", Activity: "#142113", Modal: "#142017", Border: "#6DBF67", ActivityBor: "#C7D36F", Accent: "#C7D36F", Accent2: "#6DBF67", BannerTop: "#E3ED8A", Text: "#EEF7E8", Muted: "#9CAF8F", StatusBG: "#C7D36F", StatusFG: "#101707", InputBorder: "#87B77B", CodeBG: "#1C2A19", CodeFG: "#E1F29A"},
	{ID: "ice", Name: "Icebreaker", Description: "Cool whites and arctic blues with high clarity for bright terminal sessions.", Background: "#07131A", Panel: "#0D202B", Activity: "#122B38", Modal: "#102633", Border: "#9BE7FF", ActivityBor: "#E4F8FF", Accent: "#E4F8FF", Accent2: "#9BE7FF", BannerTop: "#FFFFFF", Text: "#F2FCFF", Muted: "#A7C7D3", StatusBG: "#E4F8FF", StatusFG: "#06202A", InputBorder: "#7ED6F2", CodeBG: "#173544", CodeFG: "#D7F7FF"},
	{ID: "coffee", Name: "Coffeehouse", Description: "Espresso panels, cream text, and caramel highlights: cozy without getting sleepy.", Background: "#120D09", Panel: "#201710", Activity: "#251B13", Modal: "#2A1D14", Border: "#C58B4C", ActivityBor: "#E7C27D", Accent: "#E7C27D", Accent2: "#C58B4C", BannerTop: "#FFE0A3", Text: "#F8EBD8", Muted: "#B49B7F", StatusBG: "#E7C27D", StatusFG: "#21150B", InputBorder: "#A97845", CodeBG: "#352518", CodeFG: "#FFE1A6"},
	{ID: "bubblegum", Name: "Bubblegum Lab", Description: "Playful pink and cyan candy colors, but dark enough to remain usable.", Background: "#120A18", Panel: "#21122A", Activity: "#281631", Modal: "#281332", Border: "#FF8BD1", ActivityBor: "#7DEBFF", Accent: "#FFCA3A", Accent2: "#7DEBFF", BannerTop: "#FFE97A", Text: "#FFF2FB", Muted: "#C79BC8", StatusBG: "#7DEBFF", StatusFG: "#101829", InputBorder: "#FF8BD1", CodeBG: "#321B3B", CodeFG: "#FFE680"},
	{ID: "terminal", Name: "Terminal Blue", Description: "IBM-ish blues, clean panels, and business-machine confidence.", Background: "#071126", Panel: "#0C1B3A", Activity: "#10234A", Modal: "#0F2144", Border: "#4F8CFF", ActivityBor: "#8FD3FF", Accent: "#8FD3FF", Accent2: "#4F8CFF", BannerTop: "#CBE7FF", Text: "#EDF4FF", Muted: "#A3B8D8", StatusBG: "#8FD3FF", StatusFG: "#071B38", InputBorder: "#6FA6FF", CodeBG: "#172D57", CodeFG: "#D7EAFF"},
	{ID: "crimson", Name: "Crimson Ops", Description: "Red-alert styling with controlled contrast for incident-response drama.", Background: "#120506", Panel: "#210B0E", Activity: "#2A0D11", Modal: "#2A0C10", Border: "#FF4D5E", ActivityBor: "#FFB74D", Accent: "#FFB74D", Accent2: "#FF4D5E", BannerTop: "#FFD08A", Text: "#FFEDEF", Muted: "#BE8C91", StatusBG: "#FF4D5E", StatusFG: "#22070A", InputBorder: "#FF7A86", CodeBG: "#381116", CodeFG: "#FFD0A1"},
	{ID: "lavender", Name: "Lavender Fog", Description: "Gentle lavender and dusty blue for a softer, calmer UI without going pastel-white.", Background: "#0E0B18", Panel: "#19152A", Activity: "#201A33", Modal: "#1D1830", Border: "#B8A7FF", ActivityBor: "#8CCFFF", Accent: "#D6C8FF", Accent2: "#8CCFFF", BannerTop: "#EFE6FF", Text: "#F3F0FF", Muted: "#AAA0C8", StatusBG: "#D6C8FF", StatusFG: "#18112B", InputBorder: "#B8A7FF", CodeBG: "#28213C", CodeFG: "#E6DCFF"},
	{ID: "desert", Name: "Desert Radar", Description: "Sand, olive, and dark clay with a rugged field-computer personality.", Background: "#100E08", Panel: "#1E1A10", Activity: "#252015", Modal: "#272116", Border: "#B7A35A", ActivityBor: "#D9822B", Accent: "#E8C766", Accent2: "#8EA35A", BannerTop: "#F3DB83", Text: "#F4EBD0", Muted: "#A89B77", StatusBG: "#E8C766", StatusFG: "#1F1808", InputBorder: "#8EA35A", CodeBG: "#342B18", CodeFG: "#F5D986"},
	{ID: "neon", Name: "Neon Noir", Description: "Black glass, hot cyan, and magenta signs reflected in rainy terminal pavement.", Background: "#05070B", Panel: "#0A0F16", Activity: "#0E1420", Modal: "#0C111C", Border: "#00E5FF", ActivityBor: "#FF2EC4", Accent: "#FF2EC4", Accent2: "#00E5FF", BannerTop: "#FF85DD", Text: "#EDFBFF", Muted: "#7D9AA6", StatusBG: "#00E5FF", StatusFG: "#031218", InputBorder: "#FF2EC4", CodeBG: "#111A28", CodeFG: "#B9F7FF"},
}

var (
	headerStyle         lipgloss.Style
	blockShadowStyle    lipgloss.Style
	bannerFillStyle     lipgloss.Style
	bannerTopStyle      lipgloss.Style
	bannerFaceHighStyle lipgloss.Style
	bannerFaceLowStyle  lipgloss.Style
	bannerOutlineStyle  lipgloss.Style
	bannerShadowStyle   lipgloss.Style
	labelStyle          lipgloss.Style
	panelStyle          lipgloss.Style
	activityStyle       lipgloss.Style
	statusStyle         lipgloss.Style
	statusWorkStyle     lipgloss.Style
	spinnerStyle        lipgloss.Style
	inputBoxStyle       lipgloss.Style
	selectedInputStyle  lipgloss.Style
	selectedRowStyle    lipgloss.Style
	buttonStyle         lipgloss.Style
	modalStyle          lipgloss.Style
	modalTitleStyle     lipgloss.Style
	footerStyle         lipgloss.Style
	userStyle           lipgloss.Style
	botStyle            lipgloss.Style
	headingStyle        lipgloss.Style
	bulletStyle         lipgloss.Style
	quoteStyle          lipgloss.Style
	codeStyle           lipgloss.Style
	mutedStyle          lipgloss.Style
	completionStyle     lipgloss.Style
	currentThemePalette themePalette
)

func init() {
	applyTheme("steel")
}

func applyTheme(id string) {
	p := themeByID(id)
	currentThemePalette = p
	bg := lipgloss.Color(p.Background)
	headerStyle = lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color(p.Accent2)).Bold(true)
	blockShadowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.Accent)).Background(bg).Bold(true)
	bannerFillStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.Accent)).Background(bg).Bold(true)
	bannerTopStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.BannerTop)).Background(bg).Bold(true)
	bannerFaceHighStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.Accent)).Background(bg).Bold(true)
	bannerFaceLowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.Accent2)).Background(bg).Bold(true)
	bannerOutlineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.ActivityBor)).Background(bg).Bold(true)
	bannerShadowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.ActivityBor)).Background(bg).Bold(true)
	labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.Accent)).Bold(true).PaddingLeft(1)
	panelStyle = lipgloss.NewStyle().Border(lipgloss.ThickBorder()).BorderForeground(lipgloss.Color(p.Border)).Foreground(lipgloss.Color(p.Text)).Background(lipgloss.Color(p.Panel))
	activityStyle = lipgloss.NewStyle().Border(lipgloss.ThickBorder()).BorderForeground(lipgloss.Color(p.ActivityBor)).Foreground(lipgloss.Color(p.Text)).Background(lipgloss.Color(p.Activity))
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.StatusFG)).Background(lipgloss.Color(p.StatusBG)).Bold(true)
	statusWorkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.StatusBG)).Background(lipgloss.Color(p.StatusFG)).Bold(true)
	spinnerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.Accent)).Background(lipgloss.Color(p.StatusFG)).Bold(true)
	inputBoxStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color(p.InputBorder)).Background(lipgloss.Color(p.Background)).Padding(0, 1)
	selectedInputStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.StatusFG)).Background(lipgloss.Color(p.Accent)).Bold(true)
	selectedRowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.Text)).Background(lipgloss.Color(p.Modal)).Bold(true)
	buttonStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.StatusFG)).Background(lipgloss.Color(p.StatusBG)).Bold(true).Padding(0, 1)
	modalStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(p.Accent)).Foreground(lipgloss.Color(p.Text)).Background(lipgloss.Color(p.Modal)).Padding(1, 2)
	modalTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.Accent)).MarginBottom(1)
	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.InputBorder)).MarginTop(1)
	userStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.Accent))
	botStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.Accent2))
	headingStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.Accent))
	bulletStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.Text))
	quoteStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.InputBorder)).Italic(true)
	codeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.CodeFG)).Background(lipgloss.Color(p.CodeBG))
	mutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.Muted))
	completionStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(p.Accent2)).Foreground(lipgloss.Color(p.Text)).Background(lipgloss.Color(p.StatusFG)).Padding(0, 1)
}

func themeByID(id string) themePalette {
	for _, theme := range themes {
		if theme.ID == id {
			return theme
		}
	}
	return themes[0]
}

func themeIndexByID(id string) int {
	for i, theme := range themes {
		if theme.ID == id {
			return i
		}
	}
	return 0
}
