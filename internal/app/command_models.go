package app

import "context"

func (a *App) modelsCommand() Reply {
	reply := a.reply("Models panel opened.", "/models", "models")
	groups := DiscoverModelGroups(context.Background(), a.Config())
	reply.Data = map[string]any{
		"groups":  groups,
		"models":  FlattenModelGroups(groups),
		"current": a.Config().Model,
	}
	return reply
}
