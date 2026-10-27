package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/Bradthebrad/tinychain/agent"
)

const visualizeDescription = "Show the person something in a pop-up window in the NullBot app and collect how they respond. " +
	"Kinds: poll (options to choose from), html (a page: inline content, or a project .html file served with its folder so its own scripts, styles and images load; scripts run in a sandbox), " +
	"svg, image (a project picture; give labels to let them pin labeled points on it), chart (a Chart.js 4 config as JSON), drawio (draw.io XML or a .drawio file; they can click shapes), " +
	"and file (any other project file shown as it is, such as a PDF or video). The kind is inferred from path or content when omitted. " +
	"Clicks, form changes, keys pressed outside text fields, page loads, script errors and console errors are recorded automatically. " +
	"Page scripts can also call nullbot.record(name, data) to log something, nullbot.setResponse(data) to set what is sent, and nullbot.submit(data) to ask the person to send it. " +
	"Pages have no internet access unless allow_network is set (needs the Full permission); Chart.js is bundled at /__nullbot/lib/chart.js. " +
	"wait=true (the default for polls) waits until they press Send or Skip and returns their response, note and interactions; otherwise it returns at once and their response arrives later as a message. " +
	"Visuals are saved in the project under .nullbot/visualizations."

const visualizeGuidance = "Visuals: use `visualize` when the person asks to see a picture, chart, graph or diagram; when you need them to choose between options to learn their preference (a poll); " +
	"for labeling and feedback tasks (pinning points on an image, picking the right shape or item); to let them try a UI prototype, web page or small HTML/JavaScript game and tell you what works; " +
	"or whenever showing beats describing. What they do in it is useful data about them and about the thing shown: read their response, note and interactions (with `visualize_results` when you did not wait). " +
	"When testing a prototype, ask them in prompt to try specific controls, then check the recorded clicks and script errors to see what worked. Don't use it for answers plain text covers.\n"

func hasTool(tools []agent.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Definition().Name == name {
			return true
		}
	}
	return false
}

func visualTools(state *App) []agent.Tool {
	if !state.VisualsEnabled() {
		return nil
	}
	return []agent.Tool{visualizeTool(state), visualizeResultsTool(state)}
}

func visualizeTool(state *App) agent.Tool {
	option := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":          agent.StringProperty("Short id returned in the answer. Defaults to 1, 2, 3…"),
			"label":       agent.StringProperty("What the option says."),
			"description": agent.StringProperty("Optional detail under the label."),
			"image":       agent.StringProperty("Optional project picture shown with the option."),
		},
		"required": []string{"label"},
	}
	return agent.ToolFunc{
		Name:        "visualize",
		Description: visualizeDescription,
		Schema: agent.ToolSchema(map[string]any{
			"title":           agent.StringProperty("Short title for the window."),
			"kind":            agent.StringProperty("poll, html, svg, image, chart, drawio or file. Inferred when omitted."),
			"content":         agent.StringProperty("Inline content: HTML, SVG markup, a Chart.js config as JSON, draw.io XML, or a data:image/… URL."),
			"path":            agent.StringProperty("A project file to show instead of content: .html, .svg, a picture, .drawio, a Chart.js .json, or any file for kind file."),
			"root":            agent.StringProperty("For html: the folder served as the site root, so links such as /assets/app.js load. Defaults to the page's folder."),
			"prompt":          agent.StringProperty("What to ask or tell the person, shown above the visual: what to look at, choose, label or try."),
			"options":         map[string]any{"type": "array", "description": "Poll choices: labels, or objects with label, description and image.", "items": map[string]any{"anyOf": []any{map[string]any{"type": "string"}, option}}},
			"multiple":        map[string]any{"type": "boolean", "description": "Poll: let them choose more than one."},
			"allow_other":     map[string]any{"type": "boolean", "description": "Poll: let them write their own answer."},
			"labels":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Image: labels they can pin on points of the picture, such as defect or ok."},
			"wait":            map[string]any{"type": "boolean", "description": "Wait for their response before continuing. Defaults to true for polls, false otherwise."},
			"timeout_minutes": agent.NumberProperty("With wait: how long to wait. Defaults to 30, at most 240. The visual stays open afterwards."),
			"allow_network":   map[string]any{"type": "boolean", "description": "html or svg: let the page load from and talk to the internet (CDNs, APIs). Needs the Full permission."},
		}, "title"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			spec, err := parseVisualSpec(args)
			if err != nil {
				return "", err
			}
			live, err := state.createVisualization(ctx, spec)
			if err != nil {
				return "", err
			}
			meta := live.snapshot()
			if !meta.Wait {
				return prettyJSON(map[string]any{
					"id":       meta.ID,
					"status":   "shown",
					"kind":     meta.Kind,
					"saved_in": meta.Dir,
					"note":     "It is open in the app. When the person presses Send, their response arrives as a new message. Read their response and interactions any time with visualize_results.",
				}), nil
			}
			status := state.awaitVisual(ctx, live, spec.Timeout)
			current, err := state.Visualization(meta.ID)
			if err != nil {
				return "", err
			}
			events, _ := readVisualEvents(current.Dir, 0)
			switch status {
			case VisualSubmitted, VisualDismissed:
				return visualResult(current, events, 200, status), nil
			case "stopped":
				return visualResult(current, events, 200, "stopped: the person asked you to stop before responding"), nil
			case "canceled":
				return "", ctx.Err()
			default:
				return visualResult(current, events, 200, fmt.Sprintf("no response after %s; the visual stays open, and if they send it later it arrives as a message", spec.Timeout)), nil
			}
		},
	}
}

func visualizeResultsTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "visualize_results",
		Description: "Read a visual's status, the person's response and note, script errors, and the recorded interactions. Without id, lists recent visuals in the projects.",
		Schema: agent.ToolSchema(map[string]any{
			"id":    agent.StringProperty("Visual id returned by visualize, such as vis-20261027-091500-ab12cd."),
			"limit": agent.NumberProperty("How many of the latest interactions to include. Defaults to 300, at most 5000."),
		}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			id := strings.TrimSpace(stringArg(args, "id"))
			if id == "" {
				list := state.Visualizations("")
				if len(list) > 25 {
					list = list[:25]
				}
				rows := make([]map[string]any, 0, len(list))
				for _, v := range list {
					rows = append(rows, map[string]any{"id": v.ID, "title": v.Title, "kind": v.Kind, "status": v.Status, "project": v.ProjectName, "created_at": v.CreatedAt, "interactions": v.Events})
				}
				return prettyJSON(rows), nil
			}
			meta, err := state.Visualization(id)
			if err != nil {
				return "", err
			}
			limit := intArg(args, "limit", 300)
			if limit <= 0 {
				limit = 300
			}
			if limit > 5000 {
				limit = 5000
			}
			events, err := readVisualEvents(meta.Dir, 0)
			if err != nil {
				return "", err
			}
			status := meta.Status
			if meta.Waiting {
				status = "open, waiting for the person"
			}
			return visualResult(meta, events, limit, status), nil
		},
	}
}
