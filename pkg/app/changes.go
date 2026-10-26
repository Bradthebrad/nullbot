package app

// Change topics published by SubscribeChanges. Payloads are fetched by the
// subscriber; a topic only says "this snapshot changed".
const (
	ChangeSubmissions = "submissions"
	ChangeTasks       = "tasks"
	ChangeMCP         = "mcp"
	ChangeConfig      = "config"
	ChangeHistory     = "history"
	ChangeSchedules   = "schedules"
)

// SubscribeChanges registers fn to be told when a snapshot changes. fn must
// not block; UIs should debounce and fetch the snapshot they need.
func (a *App) SubscribeChanges(fn func(topic string)) func() {
	if fn == nil {
		return func() {}
	}
	a.mu.Lock()
	if a.changeSinks == nil {
		a.changeSinks = map[int]func(string){}
	}
	a.changeSinkSeq++
	id := a.changeSinkSeq
	a.changeSinks[id] = fn
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		delete(a.changeSinks, id)
		a.mu.Unlock()
	}
}

// notifyChange must be called without a.mu held.
func (a *App) notifyChange(topic string) {
	a.mu.Lock()
	sinks := make([]func(string), 0, len(a.changeSinks))
	for _, sink := range a.changeSinks {
		sinks = append(sinks, sink)
	}
	a.mu.Unlock()
	for _, sink := range sinks {
		sink(topic)
	}
}
