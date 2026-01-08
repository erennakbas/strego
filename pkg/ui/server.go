// Package ui provides a built-in web dashboard for strego.
package ui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/erennakbas/strego/pkg/broker"
	"github.com/erennakbas/strego/pkg/store"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Server serves the web UI dashboard.
type Server struct {
	broker broker.Broker
	store  store.Store
	logger *slog.Logger
	tmpl   *template.Template
	server *http.Server
}

// Config configures the UI server.
type Config struct {
	Addr   string
	Broker broker.Broker
	Store  store.Store
	Logger *slog.Logger
}

// NewServer creates a new UI server.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
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

	// HTMX partials
	mux.HandleFunc("GET /partials/stats", s.handlePartialStats)
	mux.HandleFunc("GET /partials/queues", s.handlePartialQueues)
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
	data := map[string]interface{}{
		"Title": "Dashboard",
		"Page":  "dashboard",
	}

	// Get stats from store if available
	if s.store != nil {
		stats, err := s.store.GetStats(r.Context())
		if err == nil {
			data["Stats"] = stats
		}
	}

	// Get queues
	queues, err := s.broker.GetQueues(r.Context())
	if err == nil {
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
			})
		}
		data["Queues"] = queueInfos
	}

	s.render(w, "layout.html", data)
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
		Limit:     50,
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
	fmt.Fprintf(w, `<span class="text-green-600">✅ Task queued for retry</span>`)
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
	fmt.Fprintf(w, `<span class="text-green-600">✅ Task deleted</span>`)
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
	fmt.Fprintf(w, `<span class="text-green-600">✅ %d tasks queued for retry</span>`, retried)
}

func (s *Server) handlePurgeDead(w http.ResponseWriter, r *http.Request) {
	queue := r.PathValue("queue")

	if err := s.broker.PurgeDLQ(r.Context(), queue); err != nil {
		s.error(w, "Failed to purge DLQ", err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Trigger", "dlqPurged")
	fmt.Fprintf(w, `<span class="text-green-600">✅ DLQ purged</span>`)
}

// HTMX partial handlers

func (s *Server) handlePartialStats(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		fmt.Fprint(w, `<div class="text-gray-500">Stats require PostgreSQL</div>`)
		return
	}

	stats, err := s.store.GetStats(r.Context())
	if err != nil {
		fmt.Fprintf(w, `<div class="text-red-500">Error: %s</div>`, err.Error())
		return
	}

	s.tmpl.ExecuteTemplate(w, "partial_stats.html", stats)
}

func (s *Server) handlePartialQueues(w http.ResponseWriter, r *http.Request) {
	queues, err := s.broker.GetQueues(r.Context())
	if err != nil {
		fmt.Fprintf(w, `<div class="text-red-500">Error: %s</div>`, err.Error())
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
		})
	}

	s.tmpl.ExecuteTemplate(w, "partial_queues.html", queueInfos)
}

func (s *Server) handlePartialTasks(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		fmt.Fprint(w, `<div class="text-gray-500">Tasks require PostgreSQL</div>`)
		return
	}

	filter := store.TaskFilter{
		Queue: r.URL.Query().Get("queue"),
		State: r.URL.Query().Get("state"),
		Limit: 20,
	}

	tasks, _, err := s.store.ListTasks(r.Context(), filter)
	if err != nil {
		fmt.Fprintf(w, `<div class="text-red-500">Error: %s</div>`, err.Error())
		return
	}

	s.tmpl.ExecuteTemplate(w, "partial_tasks.html", tasks)
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
	json.NewEncoder(w).Encode(data)
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
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
