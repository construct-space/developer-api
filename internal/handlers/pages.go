package handlers

import (
	"embed"
	"html/template"
	"log"
	"net/http"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
)

var pageTemplates map[string]*template.Template

func InitTemplates(fs embed.FS) {
	pageTemplates = make(map[string]*template.Template)

	// Public pages — use base.html layout
	publicPages := []string{
		"home.html",
		"login.html",
		"spaces.html",
		"space_detail.html",
		"publish.html",
		"docs.html",
	}
	for _, page := range publicPages {
		t, err := template.ParseFS(fs, "templates/base.html", "templates/"+page)
		if err != nil {
			log.Fatalf("Failed to parse template %s: %v", page, err)
		}
		pageTemplates[page] = t
	}

	// Author pages — use base_author.html layout with template functions
	funcMap := template.FuncMap{
		"deref": func(s *string) string {
			if s == nil {
				return ""
			}
			return *s
		},
	}
	authorPages := []string{
		"author/dashboard.html",
		"author/spaces.html",
		"author/space_detail.html",
		"author/keys.html",
		"author/profile.html",
		"author/docs.html",
		"author/docs_manifest.html",
		"author/docs_cli.html",
		"author/docs_sdk.html",
		"author/publish.html",
		"author/review.html",
		"author/data.html",
		"author/playground.html",
	}
	for _, page := range authorPages {
		t, err := template.New("").Funcs(funcMap).ParseFS(fs, "templates/author/base_author.html", "templates/"+page)
		if err != nil {
			log.Fatalf("Failed to parse author template %s: %v", page, err)
		}
		pageTemplates[page] = t
	}
}

func renderPage(w http.ResponseWriter, name string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["AppURL"] = Cfg.AppURL

	t, ok := pageTemplates[name]
	if !ok {
		http.Error(w, "Page not found", 404)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Author pages use "base_author" block, public pages use "base"
	blockName := "base"
	if len(name) > 7 && name[:7] == "author/" {
		blockName = "base_author"
	}

	if err := t.ExecuteTemplate(w, blockName, data); err != nil {
		log.Printf("Template error (%s): %v", name, err)
		http.Error(w, "Internal Server Error", 500)
	}
}

// GET /
func HomePage(w http.ResponseWriter, r *http.Request) {
	renderPage(w, "home.html", nil)
}

// GET /login
func LoginPage(w http.ResponseWriter, r *http.Request) {
	renderPage(w, "login.html", nil)
}

// GET /spaces
func SpacesPage(w http.ResponseWriter, r *http.Request) {
	renderPage(w, "spaces.html", nil)
}

// GET /spaces/{name}
func SpaceDetailPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	renderPage(w, "space_detail.html", map[string]any{"SpaceName": name})
}

// GET /publish
func PublishPage(w http.ResponseWriter, r *http.Request) {
	renderPage(w, "publish.html", nil)
}

// GET /docs
func DocsPage(w http.ResponseWriter, r *http.Request) {
	renderPage(w, "docs.html", nil)
}

// GET /docs/{topic}
func DocsTopicPage(w http.ResponseWriter, r *http.Request) {
	renderPage(w, "docs.html", nil)
}

// GET /author — dashboard overview
func AuthorDashboardPage(w http.ResponseWriter, r *http.Request) {
	session := requireSession(w, r)
	if session == nil {
		return
	}
	data := map[string]any{"User": sessionUser(session)}
	if session.UserID != nil {
		spaces := mySpaces(*session.UserID)
		data["Spaces"] = spaces
		approved := 0
		pending := 0
		downloads := 0
		for _, s := range spaces {
			switch s.Status {
			case "approved":
				approved++
			case "pending_review":
				pending++
			}
			downloads += s.Downloads
		}
		data["Stats"] = map[string]int{
			"total":     len(spaces),
			"approved":  approved,
			"pending":   pending,
			"downloads": downloads,
		}
	}
	renderPage(w, "author/dashboard.html", data)
}

// GET /author/spaces — my spaces list
func AuthorSpacesPage(w http.ResponseWriter, r *http.Request) {
	session := requireSession(w, r)
	if session == nil {
		return
	}
	data := map[string]any{"User": sessionUser(session)}
	if session.UserID != nil {
		data["Spaces"] = mySpaces(*session.UserID)
	}
	renderPage(w, "author/spaces.html", data)
}

// GET /author/spaces/{name} — manage single space
func AuthorSpaceDetailPage(w http.ResponseWriter, r *http.Request) {
	session := requireSession(w, r)
	if session == nil {
		return
	}
	name := r.PathValue("name")
	var space models.Space
	if err := database.DB.Where("name = ?", name).First(&space).Error; err != nil {
		http.Error(w, "Space not found", 404)
		return
	}
	renderPage(w, "author/space_detail.html", map[string]any{
		"User":      sessionUser(session),
		"SpaceName": name,
		"Space":     space,
	})
}

// GET /author/keys — API keys management
func AuthorKeysPage(w http.ResponseWriter, r *http.Request) {
	session := requireSession(w, r)
	if session == nil {
		return
	}
	data := map[string]any{"User": sessionUser(session)}
	if session.Email != nil {
		var publisher models.Publisher
		if err := database.DB.Where("email = ?", *session.Email).First(&publisher).Error; err == nil {
			data["Publisher"] = publisher
		}
	}
	renderPage(w, "author/keys.html", data)
}

// GET /author/profile — publisher profile
func AuthorProfilePage(w http.ResponseWriter, r *http.Request) {
	session := requireSession(w, r)
	if session == nil {
		return
	}
	data := map[string]any{"User": sessionUser(session)}
	if session.UserID != nil {
		spaces := mySpaces(*session.UserID)
		data["Spaces"] = spaces
		approved := 0
		downloads := 0
		for _, s := range spaces {
			if s.Status == "approved" {
				approved++
			}
			downloads += s.Downloads
		}
		data["Stats"] = map[string]int{
			"total":     len(spaces),
			"approved":  approved,
			"downloads": downloads,
		}
	}
	renderPage(w, "author/profile.html", data)
}

// GET /author/docs — documentation
func AuthorDocsPage(w http.ResponseWriter, r *http.Request) {
	session := requireSession(w, r)
	if session == nil {
		return
	}
	data := map[string]any{"User": sessionUser(session)}
	topic := r.PathValue("topic")
	switch topic {
	case "manifest":
		renderPage(w, "author/docs_manifest.html", data)
	case "cli":
		renderPage(w, "author/docs_cli.html", data)
	case "sdk", "data", "media":
		renderPage(w, "author/docs_sdk.html", data)
	default:
		renderPage(w, "author/docs.html", data)
	}
}

// GET /author/publish — submit a new space
func AuthorPublishPage(w http.ResponseWriter, r *http.Request) {
	session := requireSession(w, r)
	if session == nil {
		return
	}
	renderPage(w, "author/publish.html", map[string]any{"User": sessionUser(session)})
}

// GET /author/review — submission status
func AuthorReviewPage(w http.ResponseWriter, r *http.Request) {
	session := requireSession(w, r)
	if session == nil {
		return
	}
	data := map[string]any{"User": sessionUser(session)}
	if session.UserID != nil {
		spaces := mySpaces(*session.UserID)
		data["Spaces"] = spaces
		pending := 0
		approved := 0
		rejected := 0
		for _, s := range spaces {
			switch s.Status {
			case "pending_review":
				pending++
			case "approved":
				approved++
			case "rejected":
				rejected++
			}
		}
		data["Counts"] = map[string]int{
			"pending":  pending,
			"approved": approved,
			"rejected": rejected,
		}
	}
	renderPage(w, "author/review.html", data)
}
