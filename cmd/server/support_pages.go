package main

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gatstogo/web/templates/pages"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Public support pages linked from the marketing footer:
//   GET /login    find your plant's owner login (each plant's login lives
//                 on its own subdomain, so an owner who lost the link has
//                 no other way back)
//   GET /help     answers for plant owners and for customers buying gas
//   GET /contact  how to reach GatsToGo

// loginFinderRateLimit caps lookups per IP, so the finder can't be used to
// quickly enumerate plant names.
const (
	loginFinderRateLimitMax    = 30
	loginFinderRateLimitWindow = 15 * time.Minute
)

func loginFinderHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		input := strings.TrimSpace(r.URL.Query().Get("plant"))
		render := func(status int, msg string) {
			w.WriteHeader(status)
			if err := pages.LoginFinder(input, msg).Render(r.Context(), w); err != nil {
				log.Println("render login finder:", err)
			}
		}
		if input == "" {
			render(http.StatusOK, "")
			return
		}

		if allowed, err := checkIPRateLimit(r.Context(), s.Redis, "login_finder_rl:", clientIP(r), loginFinderRateLimitMax, loginFinderRateLimitWindow); err != nil {
			log.Println("login finder: rate limit check failed:", err)
		} else if !allowed {
			render(http.StatusTooManyRequests, "Too many lookups. Please wait a few minutes and try again.")
			return
		}

		slug := plantSlugFromInput(input)
		if slug == "" {
			render(http.StatusOK, "Enter your plant's web address, for example sunrise or sunrise.gatstogo.ng.")
			return
		}

		var status string
		err := s.AdminDB.QueryRow(r.Context(), `SELECT status FROM plants WHERE slug = $1`, slug).Scan(&status)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			render(http.StatusOK, "We couldn't find a plant at that address. Check the link you were given at setup, or contact us.")
			return
		case err != nil:
			log.Println("login finder lookup:", err)
			render(http.StatusOK, "Something went wrong. Please try again.")
			return
		}
		switch status {
		case "active":
			http.Redirect(w, r, tenantAbsoluteURL(s.AppBaseURL, slug, "/owner/login"), http.StatusSeeOther)
		case "provisioning":
			render(http.StatusOK, "That plant is still under review. We'll email and SMS the owner with login details once it's approved.")
		default:
			render(http.StatusOK, "That plant isn't active on GatsToGo right now. Contact us if you think this is a mistake.")
		}
	}
}

// plantSlugFromInput accepts what an owner is likely to remember -- the
// bare name ("sunrise"), the host ("sunrise.gatstogo.ng"), or a full link
// ("https://sunrise.gatstogo.ng/owner/login") -- and returns the slug.
func plantSlugFromInput(input string) string {
	s := strings.ToLower(strings.TrimSpace(input))
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil {
			s = u.Host
		}
	}
	s = strings.SplitN(s, "/", 2)[0]
	s = strings.SplitN(s, ":", 2)[0]
	s = strings.SplitN(s, ".", 2)[0]
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return ""
		}
	}
	return s
}

func helpPageHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := pages.Help(s.NotificationEmail).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func contactPageHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := pages.Contact(s.NotificationEmail).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func helpCategoryHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := pages.FindHelpCategory(chi.URLParam(r, "category"))
		if !ok {
			renderNotFound(w, r)
			return
		}
		if err := pages.HelpCategoryPage(c, s.NotificationEmail).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func helpArticleHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, a, ok := pages.FindHelpArticle(chi.URLParam(r, "category"), chi.URLParam(r, "article"))
		if !ok {
			renderNotFound(w, r)
			return
		}
		if err := pages.HelpArticlePage(c, a, s.NotificationEmail).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// helpFeedbackHandler records "Was this article helpful?" votes in the
// server log (grep "help feedback"). Only known articles are accepted, and
// it's rate-limited per IP so the log can't be flooded.
func helpFeedbackHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if allowed, err := checkIPRateLimit(r.Context(), s.Redis, "help_feedback_rl:", clientIP(r), 60, time.Hour); err == nil && !allowed {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		parts := strings.SplitN(r.FormValue("article"), "/", 2)
		helpful := r.FormValue("helpful")
		if len(parts) != 2 || (helpful != "yes" && helpful != "no") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, _, ok := pages.FindHelpArticle(parts[0], parts[1]); !ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		log.Printf("help feedback: article=%s/%s helpful=%s", parts[0], parts[1], helpful)
		w.WriteHeader(http.StatusNoContent)
	}
}
