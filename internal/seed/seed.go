package seed

import (
	"encoding/json"
	"log"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
)

func j(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func Run() {
	var count int64
	database.DB.Model(&models.Space{}).Count(&count)
	if count > 0 {
		return
	}

	spaces := []models.Space{
		{
			Name: "ai", DisplayName: "AI",
			Description: "AI-powered project assistant. Chat with multiple models, use round-robin mode, and get intelligent help with your projects.",
			Icon:        "i-lucide-sparkles", Version: "0.2.0", ScopesJSON: `["app"]`, ProjectAware: true, Author: "Construct Team", Recommended: true,
			NavigationJSON: j(map[string]any{"label": "AI", "icon": "i-lucide-sparkles", "to": "/app/ai", "order": 15}),
			PagesJSON:      j([]map[string]any{{"path": "", "label": "Chat", "icon": "i-lucide-message-square", "default": true, "toolbar": []map[string]any{{"id": "ai-new-chat", "icon": "i-lucide-plus", "label": "New Chat", "action": "new-chat"}, {"id": "ai-history", "icon": "i-lucide-history", "label": "History", "action": "history"}, {"id": "ai-models", "icon": "i-lucide-cpu", "label": "Models", "action": "models"}}}}),
			ThemeJSON:      strPtr(j(map[string]string{"color": "#A855F7", "bg": "#FAF5FF"})),
			Status:         "approved",
		},
		{
			Name: "architect", DisplayName: "Architect",
			Description: "AI-powered project planning and architecture. Design your project structure, generate documents, and plan sprints with intelligent assistance.",
			Icon:        "i-lucide-compass", Version: "0.2.0", ScopesJSON: `["app"]`, ProjectAware: true, Author: "Construct Team", Recommended: true,
			NavigationJSON: j(map[string]any{"label": "Architect", "icon": "i-lucide-compass", "to": "architect", "order": 5}),
			PagesJSON:      j([]map[string]any{{"path": "", "label": "Overview", "default": true}, {"path": "documents", "label": "Documents"}}),
			ThemeJSON:      strPtr(j(map[string]string{"color": "#8B5CF6", "bg": "#F5F3FF"})),
			Status:         "approved",
		},
		{
			Name: "code", DisplayName: "Code",
			Description: "Full-featured code editor powered by Monaco. Includes syntax highlighting, IntelliSense, integrated terminal, git operations, and AI code assistance.",
			Icon:        "i-lucide-code", Version: "0.2.0", ScopesJSON: `["app"]`, ProjectAware: true, Author: "Construct Team", Recommended: true,
			NavigationJSON: j(map[string]any{"label": "Code", "icon": "i-lucide-code", "to": "code", "order": 10}),
			PagesJSON:      j([]map[string]any{{"path": "", "label": "Files", "default": true}, {"path": "editor", "label": "Editor"}, {"path": "git", "label": "Git"}, {"path": "terminal", "label": "Terminal"}}),
			ThemeJSON:      strPtr(j(map[string]string{"color": "#3B82F6", "bg": "#EFF6FF"})),
			Status:         "approved",
		},
		{
			Name: "design", DisplayName: "Design",
			Description: "Visual design tool with a canvas-based editor. Create UI layouts, work with shapes, manage layers, and export designs. Powered by PixiJS.",
			Icon:        "i-lucide-palette", Version: "0.2.0", ScopesJSON: `["app"]`, ProjectAware: true, Author: "Construct Team", Recommended: true,
			NavigationJSON: j(map[string]any{"label": "Design", "icon": "i-lucide-palette", "to": "design", "order": 20}),
			PagesJSON:      j([]map[string]any{{"path": "", "label": "Canvas", "default": true}, {"path": "assets", "label": "Assets"}}),
			ThemeJSON:      strPtr(j(map[string]string{"color": "#EC4899", "bg": "#FDF2F8"})),
			Status:         "approved",
		},
		{
			Name: "kanban", DisplayName: "Kanban",
			Description: "Drag-and-drop task management board. Organize work into customizable columns, track progress, and manage team workflows.",
			Icon:        "i-lucide-kanban", Version: "0.2.0", ScopesJSON: `["app"]`, ProjectAware: true, Author: "Construct Team",
			NavigationJSON: j(map[string]any{"label": "Kanban", "icon": "i-lucide-kanban", "to": "kanban", "order": 30}),
			PagesJSON:      j([]map[string]any{{"path": "", "label": "Board", "default": true}}),
			ThemeJSON:      strPtr(j(map[string]string{"color": "#F59E0B", "bg": "#FFFBEB"})),
			Status:         "approved",
		},
		{
			Name: "docs", DisplayName: "Docs",
			Description: "Documentation workspace with Markdown rendering. Write, organize, and share project documentation with live preview.",
			Icon:        "i-lucide-book-open", Version: "0.2.0", ScopesJSON: `["app"]`, ProjectAware: true, Author: "Construct Team",
			NavigationJSON: j(map[string]any{"label": "Docs", "icon": "i-lucide-book-open", "to": "docs", "order": 40}),
			PagesJSON:      j([]map[string]any{{"path": "", "label": "Documents", "default": true}, {"path": "editor", "label": "Editor"}}),
			ThemeJSON:      strPtr(j(map[string]string{"color": "#10B981", "bg": "#ECFDF5"})),
			Status:         "approved",
		},
		{
			Name: "git", DisplayName: "Git",
			Description: "Git repository management. View commit history, manage branches, review diffs, and perform git operations from a visual interface.",
			Icon:        "i-lucide-git-branch", Version: "0.2.0", ScopesJSON: `["app"]`, ProjectAware: true, Author: "Construct Team",
			NavigationJSON: j(map[string]any{"label": "Git", "icon": "i-lucide-git-branch", "to": "git", "order": 50}),
			PagesJSON:      j([]map[string]any{{"path": "", "label": "Repository", "default": true}, {"path": "history", "label": "History"}, {"path": "branches", "label": "Branches"}}),
			ThemeJSON:      strPtr(j(map[string]string{"color": "#EF4444", "bg": "#FEF2F2"})),
			Status:         "approved",
		},
		{
			Name: "notes", DisplayName: "Notes",
			Description: "Quick note-taking and scratchpad. Capture ideas, meeting notes, and snippets with a clean, distraction-free interface.",
			Icon:        "i-lucide-sticky-note", Version: "0.2.0", ScopesJSON: `["app"]`, ProjectAware: true, Author: "Construct Team",
			NavigationJSON: j(map[string]any{"label": "Notes", "icon": "i-lucide-sticky-note", "to": "notes", "order": 60}),
			PagesJSON:      j([]map[string]any{{"path": "", "label": "Notes", "default": true}}),
			ThemeJSON:      strPtr(j(map[string]string{"color": "#6366F1", "bg": "#EEF2FF"})),
			Status:         "approved",
		},
		{
			Name: "calendar", DisplayName: "Calendar",
			Description: "Project calendar and scheduling. Track milestones, deadlines, and events with monthly, weekly, and daily views.",
			Icon:        "i-lucide-calendar", Version: "0.2.0", ScopesJSON: `["app"]`, ProjectAware: true, Author: "Construct Team",
			NavigationJSON: j(map[string]any{"label": "Calendar", "icon": "i-lucide-calendar", "to": "calendar", "order": 70}),
			PagesJSON:      j([]map[string]any{{"path": "", "label": "Calendar", "default": true}}),
			ThemeJSON:      strPtr(j(map[string]string{"color": "#14B8A6", "bg": "#F0FDFA"})),
			Status:         "approved",
		},
		{
			Name: "chat", DisplayName: "Chat",
			Description: "Team chat with AI-powered room agents. Create rooms, send messages in real-time, and use @mentions with AI assistance.",
			Icon:        "i-lucide-messages-square", Version: "0.2.0", ScopesJSON: `["app"]`, ProjectAware: true, Author: "Construct Team", Recommended: true,
			NavigationJSON: j(map[string]any{"label": "Chat", "icon": "i-lucide-messages-square", "to": "chat", "order": 15}),
			PagesJSON:      j([]map[string]any{{"path": "", "label": "Chat", "icon": "i-lucide-messages-square", "default": true, "toolbar": []map[string]any{{"id": "chat-new-room", "icon": "i-lucide-plus", "label": "New Room", "action": "new-room"}, {"id": "chat-members", "icon": "i-lucide-users", "label": "Members", "action": "members"}, {"id": "chat-ai", "icon": "i-lucide-sparkles", "label": "AI Settings", "action": "ai-settings"}}}}),
			ThemeJSON:      strPtr(j(map[string]string{"color": "#22C55E", "bg": "#F0FDF4"})),
			Status:         "approved",
		},
		{
			Name: "terminal", DisplayName: "Terminal",
			Description: "Integrated terminal emulator. Run commands, scripts, and interact with your development environment directly from Construct.",
			Icon:        "i-lucide-terminal", Version: "0.2.0", ScopesJSON: `["app"]`, ProjectAware: true, Author: "Construct Team",
			NavigationJSON: j(map[string]any{"label": "Terminal", "icon": "i-lucide-terminal", "to": "terminal", "order": 80}),
			PagesJSON:      j([]map[string]any{{"path": "", "label": "Terminal", "default": true}}),
			ThemeJSON:      strPtr(j(map[string]string{"color": "#64748B", "bg": "#F8FAFC"})),
			Status:         "approved",
		},
	}

	for i := range spaces {
		database.DB.Create(&spaces[i])
	}

	log.Printf("[Seed] Created %d spaces", len(spaces))
}

func strPtr(s string) *string {
	return &s
}
