// Package httpapi is the transport layer: Echo handlers over the chat
// service. sorm's typed errors map onto HTTP statuses in one place.
package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/dvislobokov/sorm"

	"github.com/dvislobokov/sorm/examples/chat/internal/models"
	"github.com/dvislobokov/sorm/examples/chat/internal/service"
)

type Handlers struct {
	chat *service.Chat
}

func Register(e *echo.Echo, chat *service.Chat) {
	h := &Handlers{chat: chat}
	api := e.Group("/api")

	api.POST("/users", h.registerUser)
	api.PUT("/users/:id/prefs", h.updatePrefs)
	api.POST("/users/:id/ban", h.banUser)
	api.GET("/users/dark-mods", h.darkMods)

	api.POST("/rooms", h.createRoom)
	api.POST("/rooms/:slug/join", h.joinRoom)
	api.GET("/rooms/:slug/messages", h.listMessages)
	api.POST("/rooms/:slug/messages", h.postMessage)
	api.GET("/rooms/:slug/stats", h.roomStats)

	api.PATCH("/messages/:id", h.editMessage)
	api.DELETE("/messages/:id", h.deleteMessage)

	api.GET("/audit", h.auditTail)
}

// httpError maps sorm's typed errors to statuses.
func httpError(err error) error {
	var constraint *sorm.ConstraintError
	var conflict *sorm.ConflictError
	switch {
	case errors.Is(err, sorm.ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	case errors.As(err, &conflict):
		return echo.NewHTTPError(http.StatusConflict, "concurrent modification — reload and retry")
	case errors.As(err, &constraint) && constraint.Kind == sorm.ConstraintUnique:
		return echo.NewHTTPError(http.StatusConflict, "already exists")
	default:
		return err
	}
}

func (h *Handlers) registerUser(c echo.Context) error {
	var req struct {
		Email string   `json:"email"`
		Name  string   `json:"name"`
		Roles []string `json:"roles"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	u, token, err := h.chat.Register(c.Request().Context(), req.Email, req.Name, req.Roles)
	if err != nil {
		return httpError(err)
	}
	return c.JSON(http.StatusCreated, echo.Map{"user": u, "token": token.ID})
}

func (h *Handlers) updatePrefs(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	var prefs models.UserPrefs
	if err := c.Bind(&prefs); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	u, err := h.chat.Users.UpdatePrefs(c.Request().Context(), id, &prefs)
	if err != nil {
		return httpError(err)
	}
	return c.JSON(http.StatusOK, u)
}

func (h *Handlers) banUser(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	if err := h.chat.Ban(c.Request().Context(), id, actorID(c)); err != nil {
		return httpError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handlers) darkMods(c echo.Context) error {
	users, err := h.chat.Users.DarkThemePushUsers(c.Request().Context())
	if err != nil {
		return httpError(err)
	}
	return c.JSON(http.StatusOK, users)
}

func (h *Handlers) createRoom(c echo.Context) error {
	var req struct {
		Slug    string `json:"slug"`
		Title   string `json:"title"`
		OwnerID int64  `json:"owner_id"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	ctx := c.Request().Context()
	owner, err := h.chat.Users.ByID(ctx, req.OwnerID)
	if err != nil {
		return httpError(err)
	}
	room := &models.Room{Slug: req.Slug, Title: req.Title, Owner: owner}
	if err := h.chat.Rooms.Create(ctx, room); err != nil {
		return httpError(err)
	}
	return c.JSON(http.StatusCreated, room)
}

func (h *Handlers) joinRoom(c echo.Context) error {
	var req struct {
		UserID int64 `json:"user_id"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	ctx := c.Request().Context()
	room, err := h.chat.Rooms.BySlug(ctx, c.Param("slug"))
	if err != nil {
		return httpError(err)
	}
	u, err := h.chat.Users.ByID(ctx, req.UserID)
	if err != nil {
		return httpError(err)
	}
	if err := h.chat.Rooms.Join(ctx, room, u); err != nil {
		return httpError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handlers) listMessages(c echo.Context) error {
	ctx := c.Request().Context()
	room, err := h.chat.Rooms.BySlug(ctx, c.Param("slug"))
	if err != nil {
		return httpError(err)
	}
	before := time.Now()
	if s := c.QueryParam("before"); s != "" {
		if before, err = time.Parse(time.RFC3339, s); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "before must be RFC3339")
		}
	}
	limit := 50
	if s := c.QueryParam("limit"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil || limit < 1 || limit > 200 {
			return echo.NewHTTPError(http.StatusBadRequest, "limit must be 1..200")
		}
	}
	msgs, err := h.chat.Msgs.Page(ctx, room.ID, before, limit)
	if err != nil {
		return httpError(err)
	}
	return c.JSON(http.StatusOK, msgs)
}

func (h *Handlers) postMessage(c echo.Context) error {
	var req struct {
		AuthorID int64                  `json:"author_id"`
		Text     string                 `json:"text"`
		ReplyTo  *int64                 `json:"reply_to"`
		Payload  *models.MessagePayload `json:"payload"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	msg, err := h.chat.Post(c.Request().Context(), c.Param("slug"), req.AuthorID, req.Text, req.Payload, req.ReplyTo)
	if err != nil {
		return httpError(err)
	}
	return c.JSON(http.StatusCreated, msg)
}

func (h *Handlers) roomStats(c echo.Context) error {
	ctx := c.Request().Context()
	room, err := h.chat.Rooms.BySlug(ctx, c.Param("slug"))
	if err != nil {
		return httpError(err)
	}
	stat, err := h.chat.Msgs.Stats(ctx, room.ID)
	if errors.Is(err, sorm.ErrNotFound) {
		return c.JSON(http.StatusOK, echo.Map{"room_id": room.ID, "messages": 0})
	}
	if err != nil {
		return httpError(err)
	}
	return c.JSON(http.StatusOK, stat)
}

func (h *Handlers) editMessage(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	msg, err := h.chat.Edit(c.Request().Context(), id, actorID(c), req.Text)
	if err != nil {
		return httpError(err)
	}
	return c.JSON(http.StatusOK, msg)
}

func (h *Handlers) deleteMessage(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	if err := h.chat.Delete(c.Request().Context(), id, actorID(c)); err != nil {
		return httpError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handlers) auditTail(c echo.Context) error {
	entries, err := h.chat.Audit.Tail(c.Request().Context(), 100)
	if err != nil {
		return httpError(err)
	}
	return c.JSON(http.StatusOK, entries)
}

// actorID — the acting user from the X-Actor-ID header (a stand-in for
// real authentication; the example is about the data layer).
func actorID(c echo.Context) int64 {
	id, _ := strconv.ParseInt(c.Request().Header.Get("X-Actor-ID"), 10, 64)
	return id
}
