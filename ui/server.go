// Package ui provides a built-in web dashboard for strego.
package ui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/erennakbas/strego/broker"
	"github.com/erennakbas/strego/store"
	"github.com/erennakbas/strego/types"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Server serves the web UI dashboard.
type Server struct {
	broker broker.Broker
	store  store.Store
	logger types.Logger
	tmpl   *template.Template
	server *http.Server
}

// Config configures the UI server.
type Config struct {
	Addr   string
	Broker broker.Broker
	Store  store.Store
	Logger types.Logger
}

// NewServer creates a new UI server.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = types.DefaultLogger()
	}

	// Parse templates
	funcMap := template.FuncMap{
		"formatTime":     formatTime,
		"formatDuration": formatDuration,
		"stateClass":     stateClass,
		"stateIcon":      stateIcon,
		"truncate":       truncate,
		"json":           toJSON,
		"add":            func(a, b int) int { return a + b },
		"sub":            func(a, b int) int { return a - b },
		"mul":            func(a, b int) int { return a * b },
		"div": func(a, b int) int {
			if b == 0 {
				return 0
			}
			return a / b
		},
		"mod": func(a, b int) int { return a % b },
		"seq": seq,
		"min": func(a, b int64) int64 {
			if a < b {
				return a
			}
			return b
		},
		"stateList":        stateList,
		"hasState":         hasState,
		"buildQueryParams": buildQueryParams,
		"pageRange":        pageRange,
	}

	tmpl, err := template.New("").Funcs(funcMap).ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse templates: %w", err)
	}

	s := &Server{
		broker: cfg.Broker,
		store:  cfg.Store,
		logger: cfg.Logger,
		tmpl:   tmpl,
	}

	mux := http.NewServeMux()

	// Static files
	staticSub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// Pages
	mux.HandleFunc("GET /", s.handleDashboard)
	mux.HandleFunc("GET /queues", s.handleQueues)
	mux.HandleFunc("GET /queues/{queue}", s.handleQueueDetail)
	mux.HandleFunc("GET /tasks", s.handleTasks)
	mux.HandleFunc("GET /tasks/{id}", s.handleTaskDetail)
	mux.HandleFunc("GET /dead", s.handleDeadLetterQueue)

	// API endpoints
	mux.HandleFunc("GET /api/stats", s.handleAPIStats)
	mux.HandleFunc("GET /api/queues", s.handleAPIQueues)
	mux.HandleFunc("GET /api/tasks", s.handleAPITasks)

	// Actions
	mux.HandleFunc("POST /tasks/{id}/retry", s.handleRetryTask)
	mux.HandleFunc("POST /tasks/{id}/delete", s.handleDeleteTask)
	mux.HandleFunc("POST /dead/{queue}/retry-all", s.handleRetryAllDead)
	mux.HandleFunc("POST /dead/{queue}/purge", s.handlePurgeDead)
	mux.HandleFunc("POST /admin/cleanup-consumers", s.handleCleanupConsumers)

	// HTMX partials
	mux.HandleFunc("GET /partials/stats", s.handlePartialStats)
	mux.HandleFunc("GET /partials/queues", s.handlePartialQueues)
	mux.HandleFunc("GET /partials/consumers", s.handlePartialConsumers)
	mux.HandleFunc("GET /partials/tasks", s.handlePartialTasks)

	s.server = &http.Server{
		Addr:         cfg.Addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return s, nil
}

// Start starts the UI server.
func (s *Server) Start() error {
	s.logger.Info("starting UI server", "addr", s.server.Addr)
	return s.server.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// Helper functions

func formatTime(t interface{}) string {
	switch v := t.(type) {
	case time.Time:
		if v.IsZero() {
			return "-"
		}
		return v.Format("2006-01-02 15:04:05")
	case *time.Time:
		if v == nil || v.IsZero() {
			return "-"
		}
		return v.Format("2006-01-02 15:04:05")
	default:
		return "-"
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.1fm", d.Minutes())
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}

func stateClass(state string) string {
	switch state {
	case "pending":
		return "bg-yellow-100 text-yellow-800"
	case "scheduled":
		return "bg-blue-100 text-blue-800"
	case "active":
		return "bg-cyan-100 text-cyan-800"
	case "completed":
		return "bg-green-100 text-green-800"
	case "failed", "dead":
		return "bg-red-100 text-red-800"
	case "retry":
		return "bg-orange-100 text-orange-800"
	case "cancelled":
		return "bg-gray-100 text-gray-800"
	default:
		return "bg-gray-100 text-gray-800"
	}
}

func stateIcon(state string) string {
	switch state {
	case "pending":
		return "⏳"
	case "scheduled":
		return "📅"
	case "active":
		return "⚡"
	case "completed":
		return "✅"
	case "failed":
		return "❌"
	case "dead":
		return "💀"
	case "retry":
		return "🔄"
	case "cancelled":
		return "🚫"
	default:
		return "❓"
	}
}

func truncate(s string, length int) string {
	if len(s) <= length {
		return s
	}
	return s[:length] + "..."
}

func toJSON(v interface{}) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}

func seq(start, end int) []int {
	if end < start {
		return nil
	}
	result := make([]int, end-start+1)
	for i := range result {
		result[i] = start + i
	}
	return result
}

// StateInfo represents a task state for the filter UI
type StateInfo struct {
	Value          string
	Label          string
	Icon           string
	IconClass      string
	CheckedClass   string
	UncheckedClass string
}

func stateList() []StateInfo {
	return []StateInfo{
		{
			Value:          "pending",
			Label:          "Pending",
			Icon:           "●",
			IconClass:      "text-amber-400",
			CheckedClass:   "bg-amber-500/20 text-amber-300",
			UncheckedClass: "bg-gray-800/50 text-gray-400",
		},
		{
			Value:          "active",
			Label:          "Active",
			Icon:           "●",
			IconClass:      "text-cyan-400 animate-pulse",
			CheckedClass:   "bg-cyan-500/20 text-cyan-300",
			UncheckedClass: "bg-gray-800/50 text-gray-400",
		},
		{
			Value:          "completed",
			Label:          "Completed",
			Icon:           "✓",
			IconClass:      "text-emerald-400",
			CheckedClass:   "bg-emerald-500/20 text-emerald-300",
			UncheckedClass: "bg-gray-800/50 text-gray-400",
		},
		{
			Value:          "retry",
			Label:          "Retry",
			Icon:           "↻",
			IconClass:      "text-orange-400",
			CheckedClass:   "bg-orange-500/20 text-orange-300",
			UncheckedClass: "bg-gray-800/50 text-gray-400",
		},
		{
			Value:          "failed",
			Label:          "Failed",
			Icon:           "✕",
			IconClass:      "text-red-400",
			CheckedClass:   "bg-red-500/20 text-red-300",
			UncheckedClass: "bg-gray-800/50 text-gray-400",
		},
		{
			Value:          "dead",
			Label:          "Dead",
			Icon:           "⚠",
			IconClass:      "text-rose-400",
			CheckedClass:   "bg-rose-500/20 text-rose-300",
			UncheckedClass: "bg-gray-800/50 text-gray-400",
		},
	}
}

func hasState(states []string, state string) bool {
	for _, s := range states {
		if s == state {
			return true
		}
	}
	return false
}

func buildQueryParams(filter store.TaskFilter) string {
	params := ""
	if filter.Queue != "" {
		params += "&queue=" + filter.Queue
	}
	if filter.Search != "" {
		params += "&search=" + filter.Search
	}
	for _, s := range filter.States {
		params += "&states=" + s
	}
	if filter.Limit > 0 && filter.Limit != 20 {
		params += "&limit=" + strconv.Itoa(filter.Limit)
	}
	return params
}

func pageRange(current, total, maxVisible int) []int {
	if total <= maxVisible {
		return seq(1, total)
	}

	half := maxVisible / 2
	start := current - half
	end := current + half

	if start < 1 {
		start = 1
		end = maxVisible
	}
	if end > total {
		end = total
		start = total - maxVisible + 1
	}

	return seq(start, end)
}

// Page handlers

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get selected group from query param
	selectedGroup := r.URL.Query().Get("group")

	// Get all queues
	queues, err := s.broker.GetQueues(ctx)
	if err != nil {
		s.error(w, "Failed to get queues", err, http.StatusInternalServerError)
		return
	}

	// Collect all consumer groups across all queues
	groupsMap := make(map[string]map[string]bool) // group name -> consumer names

	for _, queue := range queues {
		groups, err := s.broker.GetConsumerGroups(ctx, queue)
		if err != nil {
			continue
		}
		for _, group := range groups {
			if groupsMap[group.Name] == nil {
				groupsMap[group.Name] = make(map[string]bool)
			}
			// Track unique consumers across all queues for this group
			consumers, _ := s.broker.GetGroupConsumers(ctx, queue, group.Name)
			for _, c := range consumers {
				groupsMap[group.Name][c.Name] = true
			}
		}
	}

	// Convert to slice
	type ConsumerGroupSummary struct {
		Name          string
		ConsumerCount int
	}
	var consumerGroups []ConsumerGroupSummary
	for groupName, consumerNames := range groupsMap {
		consumerGroups = append(consumerGroups, ConsumerGroupSummary{
			Name:          groupName,
			ConsumerCount: len(consumerNames),
		})
	}

	// Sort consumer groups alphabetically by name
	sort.Slice(consumerGroups, func(i, j int) bool {
		return consumerGroups[i].Name < consumerGroups[j].Name
	})

	// If no group selected, use first one (now alphabetically first)
	if selectedGroup == "" && len(consumerGroups) > 0 {
		selectedGroup = consumerGroups[0].Name
	}

	// Get queue info for selected group
	var queueInfos []map[string]interface{}
	type ConsumerDisplay struct {
		Name        string
		Pending     int64
		IdleTime    string
		Status      string
		StatusClass string
	}
	var consumers []ConsumerDisplay

	if selectedGroup != "" {
		// Get queue infos for this group
		for _, queue := range queues {
			queueInfo := s.getQueueInfoForGroup(ctx, queue, selectedGroup)
			if queueInfo != nil {
				queueInfos = append(queueInfos, queueInfo)
			}
		}

		// Get consumers for this group from all queues
		consumersMap := make(map[string]types.ConsumerInfo)
		for _, queue := range queues {
			consumerList, err := s.broker.GetGroupConsumers(ctx, queue, selectedGroup)
			if err != nil {
				continue
			}
			for _, c := range consumerList {
				if existing, found := consumersMap[c.Name]; !found {
					// First time seeing this consumer
					consumersMap[c.Name] = *c
				} else {
					// Aggregate: use least idle time and sum pending tasks
					if c.Idle < existing.Idle {
						existing.Idle = c.Idle
					}
					existing.Pending += c.Pending
					consumersMap[c.Name] = existing
				}
			}
		}

		// Convert to display format
		for _, c := range consumersMap {
			status := "🟢 Active"
			statusClass := "bg-green-900/30 text-green-400"

			// Determine status based on idle time
			idleSeconds := c.Idle / 1000
			if idleSeconds > 600 { // > 10 minutes
				status = "🔴 Dead"
				statusClass = "bg-red-900/30 text-red-400"
			} else if idleSeconds > 60 { // > 1 minute
				status = "🟡 Idle"
				statusClass = "bg-yellow-900/30 text-yellow-400"
			}

			consumers = append(consumers, ConsumerDisplay{
				Name:        c.Name,
				Pending:     c.Pending,
				IdleTime:    s.formatIdleTime(c.Idle),
				Status:      status,
				StatusClass: statusClass,
			})
		}
	}

	data := map[string]interface{}{
		"Title":          "Dashboard",
		"Page":           "dashboard",
		"ConsumerGroups": consumerGroups,
		"SelectedGroup":  selectedGroup,
		"Queues":         queueInfos,
		"Consumers":      consumers,
	}

	// Get stats from store if available
	if s.store != nil {
		stats, err := s.store.GetStats(ctx)
		if err == nil {
			data["Stats"] = stats
		}
	}

	s.render(w, "layout.html", data)
}

// Helper function to get queue info for a specific consumer group
func (s *Server) getQueueInfoForGroup(ctx context.Context, queue, group string) map[string]interface{} {
	// Check if this queue has the selected consumer group
	groups, err := s.broker.GetConsumerGroups(ctx, queue)
	if err != nil {
		return nil
	}

	var groupInfo *types.ConsumerGroupInfo
	for _, g := range groups {
		if g.Name == group {
			groupInfo = g
			break
		}
	}

	if groupInfo == nil {
		return nil // This queue doesn't have the selected group
	}

	// Get full queue info (this uses broker's own consumer group, but Dead/Processed are global)
	info, err := s.broker.GetQueueInfo(ctx, queue)
	if err != nil {
		return nil
	}

	return map[string]interface{}{
		"Name":      queue,
		"Pending":   info.Pending,
		"Active":    info.Active,
		"Scheduled": info.Scheduled,
		"Retry":     info.Retry,
		"Dead":      info.Dead,
		"Processed": info.Processed,
	}
}

// Helper function to format idle time
func (s *Server) formatIdleTime(idleMs int64) string {
	if idleMs < 1000 {
		return fmt.Sprintf("%dms", idleMs)
	} else if idleMs < 60000 {
		return fmt.Sprintf("%ds", idleMs/1000)
	} else if idleMs < 3600000 {
		return fmt.Sprintf("%dm", idleMs/60000)
	}
	return fmt.Sprintf("%dh", idleMs/3600000)
}

func (s *Server) handleQueues(w http.ResponseWriter, r *http.Request) {
	queues, err := s.broker.GetQueues(r.Context())
	if err != nil {
		s.error(w, "Failed to get queues", err, http.StatusInternalServerError)
		return
	}

	queueInfos := make([]map[string]interface{}, 0, len(queues))
	for _, q := range queues {
		info, err := s.broker.GetQueueInfo(r.Context(), q)
		if err != nil {
			continue
		}
		queueInfos = append(queueInfos, map[string]interface{}{
			"Name":      info.Name,
			"Pending":   info.Pending,
			"Active":    info.Active,
			"Dead":      info.Dead,
			"Processed": info.Processed,
			"Failed":    info.Failed,
		})
	}

	data := map[string]interface{}{
		"Title":  "Queues",
		"Page":   "queues",
		"Queues": queueInfos,
	}

	s.render(w, "layout.html", data)
}

func (s *Server) handleQueueDetail(w http.ResponseWriter, r *http.Request) {
	queue := r.PathValue("queue")

	info, err := s.broker.GetQueueInfo(r.Context(), queue)
	if err != nil {
		s.error(w, "Failed to get queue info", err, http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title": fmt.Sprintf("Queue: %s", queue),
		"Page":  "queue-detail",
		"Queue": info,
	}

	// Get tasks from store if available
	if s.store != nil {
		tasks, _, err := s.store.ListTasks(r.Context(), store.TaskFilter{
			Queue: queue,
			Limit: 50,
		})
		if err == nil {
			data["Tasks"] = tasks
		}
	}

	s.render(w, "layout.html", data)
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		s.error(w, "Task history requires PostgreSQL store", nil, http.StatusServiceUnavailable)
		return
	}

	// Parse query params
	filter := store.TaskFilter{
		Queue:     r.URL.Query().Get("queue"),
		States:    r.URL.Query()["states"], // Multiple states
		Type:      r.URL.Query().Get("type"),
		Search:    r.URL.Query().Get("search"),
		SortBy:    r.URL.Query().Get("sort"),
		SortOrder: r.URL.Query().Get("order"),
		Limit:     20, // default
	}

	// Parse limit with validation
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			// Limit between 1 and 100
			if limit > 100 {
				limit = 100
			}
			filter.Limit = limit
		}
	}

	// Backwards compatibility: single state param
	if state := r.URL.Query().Get("state"); state != "" && len(filter.States) == 0 {
		filter.States = []string{state}
	}

	if page := r.URL.Query().Get("page"); page != "" {
		if p, err := strconv.Atoi(page); err == nil && p > 1 {
			filter.Offset = (p - 1) * filter.Limit
		}
	}

	tasks, total, err := s.store.ListTasks(r.Context(), filter)
	if err != nil {
		s.error(w, "Failed to list tasks", err, http.StatusInternalServerError)
		return
	}

	// Get queues for filter dropdown
	queues, _ := s.broker.GetQueues(r.Context())

	// Calculate pagination
	currentPage := (filter.Offset / filter.Limit) + 1
	totalPages := int((total + int64(filter.Limit) - 1) / int64(filter.Limit))

	data := map[string]interface{}{
		"Title":       "Tasks",
		"Page":        "tasks",
		"Tasks":       tasks,
		"Total":       total,
		"CurrentPage": currentPage,
		"TotalPages":  totalPages,
		"Filter":      filter,
		"Queues":      queues,
	}

	// If HTMX request, render only the table partial
	if r.Header.Get("HX-Request") == "true" {
		s.render(w, "tasks_table.html", data)
		return
	}

	s.render(w, "layout.html", data)
}

func (s *Server) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")

	if s.store == nil {
		s.error(w, "Task detail requires PostgreSQL store", nil, http.StatusServiceUnavailable)
		return
	}

	task, err := s.store.GetTask(r.Context(), taskID)
	if err != nil {
		s.error(w, "Task not found", err, http.StatusNotFound)
		return
	}

	data := map[string]interface{}{
		"Title": fmt.Sprintf("Task: %s", truncate(taskID, 8)),
		"Page":  "task-detail",
		"Task":  task,
	}

	s.render(w, "layout.html", data)
}

func (s *Server) handleDeadLetterQueue(w http.ResponseWriter, r *http.Request) {
	queues, err := s.broker.GetQueues(r.Context())
	if err != nil {
		s.error(w, "Failed to get queues", err, http.StatusInternalServerError)
		return
	}

	deadTasks := make(map[string]interface{})
	for _, q := range queues {
		tasks, err := s.broker.GetDLQ(r.Context(), q, 100)
		if err != nil {
			continue
		}
		if len(tasks) > 0 {
			deadTasks[q] = tasks
		}
	}

	// If HTMX request, render only the content partial
	if r.Header.Get("HX-Request") == "true" {
		_ = s.tmpl.ExecuteTemplate(w, "partial_dead_content.html", deadTasks)
		return
	}

	data := map[string]interface{}{
		"Title":     "Dead Letter Queue",
		"Page":      "dead",
		"DeadTasks": deadTasks,
	}

	s.render(w, "layout.html", data)
}

// API handlers

func (s *Server) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		s.jsonError(w, "Stats require PostgreSQL store", http.StatusServiceUnavailable)
		return
	}

	stats, err := s.store.GetStats(r.Context())
	if err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.json(w, stats)
}

func (s *Server) handleAPIQueues(w http.ResponseWriter, r *http.Request) {
	queues, err := s.broker.GetQueues(r.Context())
	if err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result := make([]map[string]interface{}, 0, len(queues))
	for _, q := range queues {
		info, err := s.broker.GetQueueInfo(r.Context(), q)
		if err != nil {
			continue
		}
		result = append(result, map[string]interface{}{
			"name":      info.Name,
			"pending":   info.Pending,
			"active":    info.Active,
			"dead":      info.Dead,
			"processed": info.Processed,
		})
	}

	s.json(w, result)
}

func (s *Server) handleAPITasks(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		s.jsonError(w, "Task listing requires PostgreSQL store", http.StatusServiceUnavailable)
		return
	}

	filter := store.TaskFilter{
		Queue: r.URL.Query().Get("queue"),
		State: r.URL.Query().Get("state"),
		Limit: 50,
	}

	tasks, total, err := s.store.ListTasks(r.Context(), filter)
	if err != nil {
		s.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.json(w, map[string]interface{}{
		"tasks": tasks,
		"total": total,
	})
}

// Action handlers

func (s *Server) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	queue := r.URL.Query().Get("queue")

	if queue == "" {
		queue = "default"
	}

	if err := s.broker.RetryFromDLQ(r.Context(), queue, taskID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			s.error(w, "Task not found in DLQ", err, http.StatusNotFound)
		} else {
			s.error(w, "Failed to retry task", err, http.StatusInternalServerError)
		}
		return
	}

	// Update DB state to pending
	if s.store != nil {
		if err := s.store.UpdateTaskState(r.Context(), taskID, "pending", ""); err != nil {
			s.logger.Warn("failed to update task state in store", "task_id", taskID, "error", err)
		}
	}

	// Return success for HTMX
	w.Header().Set("HX-Trigger", "taskRetried")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<span class="text-green-600">✅ Task queued for retry</span>`)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")

	if s.store == nil {
		s.error(w, "Delete requires PostgreSQL store", nil, http.StatusServiceUnavailable)
		return
	}

	if err := s.store.DeleteTask(r.Context(), taskID); err != nil {
		s.error(w, "Failed to delete task", err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Trigger", "taskDeleted")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<span class="text-green-600">✅ Task deleted</span>`)
}

func (s *Server) handleRetryAllDead(w http.ResponseWriter, r *http.Request) {
	queue := r.PathValue("queue")

	tasks, err := s.broker.GetDLQ(r.Context(), queue, 1000)
	if err != nil {
		s.error(w, "Failed to get DLQ", err, http.StatusInternalServerError)
		return
	}

	retried := 0
	for _, task := range tasks {
		if err := s.broker.RetryFromDLQ(r.Context(), queue, task.ID); err == nil {
			retried++
		}
	}

	w.Header().Set("HX-Trigger", "dlqCleared")
	_, _ = fmt.Fprintf(w, `<span class="text-green-600">✅ %d tasks queued for retry</span>`, retried)
}

func (s *Server) handlePurgeDead(w http.ResponseWriter, r *http.Request) {
	queue := r.PathValue("queue")

	if err := s.broker.PurgeDLQ(r.Context(), queue); err != nil {
		s.error(w, "Failed to purge DLQ", err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Trigger", "dlqPurged")
	_, _ = fmt.Fprintf(w, `<span class="text-green-600">✅ DLQ purged</span>`)
}

func (s *Server) handleCleanupConsumers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	threshold := 10 * time.Minute // Manual cleanup threshold matching UI display

	// Get selected group from query param or form
	selectedGroup := r.URL.Query().Get("group")
	if selectedGroup == "" {
		selectedGroup = r.FormValue("group")
	}

	queues, err := s.broker.GetQueues(ctx)
	if err != nil {
		s.error(w, "Failed to get queues", err, http.StatusInternalServerError)
		return
	}

	var totalRemoved int
	var removedDetails []string

	for _, queue := range queues {
		groups, err := s.broker.GetConsumerGroups(ctx, queue)
		if err != nil {
			continue
		}

		for _, group := range groups {
			// Skip if not the selected group
			if selectedGroup != "" && group.Name != selectedGroup {
				continue
			}
			consumers, err := s.broker.GetGroupConsumers(ctx, queue, group.Name)
			if err != nil {
				continue
			}

			for _, c := range consumers {
				// Safety checks: only remove if no pending tasks and idle > threshold
				shouldCleanup := c.Pending == 0 && c.Idle > int64(threshold.Milliseconds())

				if shouldCleanup {
					err := s.broker.RemoveConsumer(ctx, queue, group.Name, c.Name)
					if err != nil {
						s.logger.Error("failed to remove consumer",
							"queue", queue,
							"group", group.Name,
							"consumer", c.Name,
							"error", err)
						continue
					}

					totalRemoved++
					removedDetails = append(removedDetails, fmt.Sprintf("%s/%s/%s", queue, group.Name, c.Name))
					s.logger.Info("manually removed dead consumer",
						"queue", queue,
						"group", group.Name,
						"consumer", c.Name,
						"idle_minutes", c.Idle/60000)
				}
			}
		}
	}

	// Count skipped consumers with pending tasks
	skippedWithPending := 0
	for _, queue := range queues {
		groups, _ := s.broker.GetConsumerGroups(ctx, queue)
		for _, group := range groups {
			if selectedGroup != "" && group.Name != selectedGroup {
				continue
			}
			consumers, _ := s.broker.GetGroupConsumers(ctx, queue, group.Name)
			for _, c := range consumers {
				idleMinutes := float64(c.Idle) / 60000.0
				if c.Pending > 0 && idleMinutes > 10 {
					skippedWithPending++
				}
			}
		}
	}

	w.Header().Set("HX-Trigger", "refresh")
	if totalRemoved > 0 {
		_, _ = fmt.Fprintf(w, `Cleaned up %d dead consumer(s) from Redis`, totalRemoved)
		if skippedWithPending > 0 {
			_, _ = fmt.Fprintf(w, ` • Skipped %d with pending tasks in PEL`, skippedWithPending)
		}
	} else if skippedWithPending > 0 {
		_, _ = fmt.Fprintf(w, `Found %d dead consumer(s) with pending tasks • Auto-recovery via XAUTOCLAIM will handle these`, skippedWithPending)
	} else {
		_, _ = fmt.Fprintf(w, `No dead consumers found in consumer group`)
	}
}

// HTMX partial handlers

func (s *Server) handlePartialStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Build stats from broker (Redis) if store is not available
	var stats struct {
		TotalTasks     int64
		PendingTasks   int64
		ActiveTasks    int64
		CompletedTasks int64
		FailedTasks    int64
		DeadTasks      int64
		RetryTasks     int64
		ScheduledTasks int64
	}

	// Get stats from store (PostgreSQL) if available
	if s.store != nil {
		storeStats, err := s.store.GetStats(ctx)
		if err == nil {
			stats.TotalTasks = storeStats.TotalTasks
			stats.PendingTasks = storeStats.PendingTasks
			stats.ActiveTasks = storeStats.ActiveTasks
			stats.CompletedTasks = storeStats.CompletedTasks
			stats.FailedTasks = storeStats.FailedTasks
			stats.DeadTasks = storeStats.DeadTasks
			stats.RetryTasks = storeStats.RetryTasks
			stats.ScheduledTasks = storeStats.ScheduledTasks
		}
	}

	// Always get scheduled/retry counts from Redis (real-time)
	queues, err := s.broker.GetQueues(ctx)
	if err == nil {
		for _, queue := range queues {
			scheduledCount, _ := s.broker.GetScheduledCount(ctx, queue)
			retryCount, _ := s.broker.GetRetryCount(ctx, queue)
			stats.ScheduledTasks += scheduledCount
			stats.RetryTasks += retryCount

			// If no store, also get pending/active/dead from broker
			if s.store == nil {
				queueInfo, err := s.broker.GetQueueInfo(ctx, queue)
				if err == nil {
					stats.PendingTasks += queueInfo.Pending
					stats.ActiveTasks += queueInfo.Active
					stats.DeadTasks += queueInfo.Dead
					stats.CompletedTasks += queueInfo.Completed
				}
			}
		}
	}

	_ = s.tmpl.ExecuteTemplate(w, "partial_stats.html", stats)
}

func (s *Server) handlePartialQueues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	selectedGroup := r.URL.Query().Get("group")

	queues, err := s.broker.GetQueues(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(w, `<div class="text-red-500">Error: %s</div>`, err.Error())
		return
	}

	var queueInfos []map[string]interface{}
	if selectedGroup != "" {
		// Get queue info for selected group
		for _, queue := range queues {
			queueInfo := s.getQueueInfoForGroup(ctx, queue, selectedGroup)
			if queueInfo != nil {
				queueInfos = append(queueInfos, queueInfo)
			}
		}
	} else {
		// Get all queue infos
		for _, q := range queues {
			info, err := s.broker.GetQueueInfo(ctx, q)
			if err != nil {
				continue
			}
			queueInfos = append(queueInfos, map[string]interface{}{
				"Name":      info.Name,
				"Pending":   info.Pending,
				"Active":    info.Active,
				"Scheduled": info.Scheduled,
				"Retry":     info.Retry,
				"Dead":      info.Dead,
				"Processed": info.Processed,
			})
		}
	}

	if len(queueInfos) == 0 {
		_, _ = fmt.Fprint(w, `<div class="p-8 text-center">
			<div class="text-gray-600 mb-2">📭</div>
			<p class="text-gray-500 text-sm">No queues found for this consumer group</p>
			<p class="text-gray-600 text-xs mt-1">Try selecting a different group from the dropdown above</p>
		</div>`)
		return
	}

	_ = s.tmpl.ExecuteTemplate(w, "partial_queues.html", queueInfos)
}

func (s *Server) handlePartialConsumers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	selectedGroup := r.URL.Query().Get("group")

	if selectedGroup == "" {
		_, _ = fmt.Fprint(w, `<div class="text-gray-500 text-sm">No consumer group selected</div>`)
		return
	}

	queues, err := s.broker.GetQueues(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(w, `<div class="text-red-500">Error: %s</div>`, err.Error())
		return
	}

	// Get consumers for this group from all queues
	type ConsumerDisplay struct {
		Name        string
		Pending     int64
		IdleTime    string
		Status      string
		StatusClass string
	}

	consumersMap := make(map[string]types.ConsumerInfo)
	for _, queue := range queues {
		consumerList, err := s.broker.GetGroupConsumers(ctx, queue, selectedGroup)
		if err != nil {
			continue
		}
		for _, c := range consumerList {
			if existing, found := consumersMap[c.Name]; !found {
				// First time seeing this consumer
				consumersMap[c.Name] = *c
			} else {
				// Aggregate: use least idle time and sum pending tasks
				if c.Idle < existing.Idle {
					existing.Idle = c.Idle
				}
				existing.Pending += c.Pending
				consumersMap[c.Name] = existing
			}
		}
	}

	// Convert to display format
	var consumers []ConsumerDisplay
	for _, c := range consumersMap {
		status := "🟢 Active"
		statusClass := "bg-green-900/30 text-green-400"

		// Determine status based on idle time
		idleSeconds := c.Idle / 1000
		if idleSeconds > 600 { // > 10 minutes
			status = "🔴 Dead"
			statusClass = "bg-red-900/30 text-red-400"
		} else if idleSeconds > 60 { // > 1 minute
			status = "🟡 Idle"
			statusClass = "bg-yellow-900/30 text-yellow-400"
		}

		consumers = append(consumers, ConsumerDisplay{
			Name:        c.Name,
			Pending:     c.Pending,
			IdleTime:    s.formatIdleTime(c.Idle),
			Status:      status,
			StatusClass: statusClass,
		})
	}

	// Render consumers
	for _, consumer := range consumers {
		_, _ = fmt.Fprintf(w, `<div class="flex items-center justify-between p-3 bg-gray-800/50 rounded-lg hover:bg-gray-800/70 transition-colors">
			<div class="flex items-center gap-3 flex-1">
				<span class="inline-flex items-center gap-1.5 px-2 py-1 rounded text-xs %s font-medium">%s</span>
				<span class="text-sm font-mono text-strego-400">%s</span>
			</div>
			<div class="flex items-center gap-4 text-xs">
				<div class="flex items-center gap-1.5">
					<span class="text-gray-400">Pending in PEL: <span class="text-yellow-400 font-medium">%d</span></span>
					<div class="relative inline-flex items-center group">
						<svg class="w-3.5 h-3.5 text-gray-600 hover:text-gray-400 cursor-help" fill="currentColor" viewBox="0 0 20 20">
							<path fill-rule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7-4a1 1 0 11-2 0 1 1 0 012 0zM9 9a1 1 0 000 2v3a1 1 0 001 1h1a1 1 0 100-2v-3a1 1 0 00-1-1H9z" clip-rule="evenodd"/>
						</svg>
						<div class="absolute top-full left-1/2 -translate-x-1/2 mt-2 hidden group-hover:block z-50">
							<div class="bg-gray-800 text-white text-xs rounded py-1.5 px-2 w-48 shadow-xl border border-gray-700 whitespace-nowrap">
								PEL = Pending Entries List (UNACKED tasks in Redis)
							</div>
						</div>
					</div>
				</div>
				<span class="text-gray-400">Idle Time: <span class="text-gray-300 font-medium">%s</span></span>
			</div>
		</div>`, consumer.StatusClass, consumer.Status, consumer.Name, consumer.Pending, consumer.IdleTime)
	}
}

func (s *Server) handlePartialTasks(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		_, _ = fmt.Fprint(w, `<div class="text-gray-500">Tasks require PostgreSQL</div>`)
		return
	}

	filter := store.TaskFilter{
		Queue: r.URL.Query().Get("queue"),
		State: r.URL.Query().Get("state"),
		Limit: 20,
	}

	tasks, _, err := s.store.ListTasks(r.Context(), filter)
	if err != nil {
		_, _ = fmt.Fprintf(w, `<div class="text-red-500">Error: %s</div>`, err.Error())
		return
	}

	_ = s.tmpl.ExecuteTemplate(w, "partial_tasks.html", tasks)
}

// Helpers

func (s *Server) render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.logger.Error("template error", "template", name, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (s *Server) json(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) error(w http.ResponseWriter, message string, err error, status int) {
	if err != nil {
		s.logger.Error(message, "error", err)
	}
	http.Error(w, message, status)
}

func (s *Server) jsonError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
