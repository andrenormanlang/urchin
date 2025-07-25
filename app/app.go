package app

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/matheusgomes28/urchin/common"
	"github.com/matheusgomes28/urchin/database"
	"github.com/matheusgomes28/urchin/views"
	"github.com/rs/zerolog/log"
)

const CACHE_TIMEOUT = 20 * time.Second

type Generator = func(*gin.Context, common.AppSettings, database.Database) ([]byte, error)

// func permalinkPostHandler(c *gin.Context, app_settings common.AppSettings, db database.Database) ([]byte, error) {
func permalinkPostHandler(post_id int) func(*gin.Context, common.AppSettings, database.Database) ([]byte, error) {
	return func(c *gin.Context, app_settings common.AppSettings, database database.Database) ([]byte, error) {
		c.Params = append(c.Params, gin.Param{Key: "id", Value: fmt.Sprintf("%d", post_id)})
		return postHandler(c, app_settings, database)
	}
}

func SetupRoutes(app_settings common.AppSettings, database database.Database) *gin.Engine {
	r := gin.Default()
	r.MaxMultipartMemory = 1

	// All cache-able endpoints
	cache := MakeCache(4, time.Minute*10, &TimeValidator{})
	addCachableHandler(r, "GET", "/", homeHandler, &cache, app_settings, database)
	addCachableHandler(r, "GET", "/contact", contactHandler, &cache, app_settings, database)
	addCachableHandler(r, "GET", "/about", aboutHandler, &cache, app_settings, database)
	addCachableHandler(r, "GET", "/services", servicesHandler, &cache, app_settings, database)
	addCachableHandler(r, "GET", "/post/:id", postHandler, &cache, app_settings, database)
	addCachableHandler(r, "GET", "/images/:name", imageHandler, &cache, app_settings, database)
	addCachableHandler(r, "GET", "/images", imagesHandler, &cache, app_settings, database)
	addCachableHandler(r, "GET", "/gallery/:name", galleryHandler, &cache, app_settings, database)

	permalinks, err := database.GetPermalinks()
	if err != nil {
		log.Error().Msgf("could not get permalinks: %v", err)
	} else {
		for _, permalink := range permalinks {
			addCachableHandler(r, "GET", permalink.Path, permalinkPostHandler(permalink.PostId), &cache, app_settings, database)
		}
	}

	// Pages will be querying the page content from the unique
	// link given at the creation of the page step
	addCachableHandler(r, "GET", "/pages/:link", pageHandler, &cache, app_settings, database)

	// Static endpoint for image serving
	r.Static("/images/data", app_settings.ImageDirectory)

	// Add the pagination route as a cacheable endpoint
	addCachableHandler(r, "GET", "/page/:num", homeHandler, &cache, app_settings, database)

	// DO not cache as it needs to handlenew form values
	r.POST("/contact-send", makeContactFormHandler(app_settings))

	// Where all the static files (css, js, etc) are served from
	r.Static("/static", "./static")

	// For container health purposes
	r.Any("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, common.Page{})
	})

	r.NoRoute(NotFoundHandler(app_settings))

	return r
}

func addCachableHandler(e *gin.Engine, method string, endpoint string, generator Generator, cache *Cache, app_settings common.AppSettings, db database.Database) {

	handler := func(c *gin.Context) {
		// if the endpoint is cached
		if app_settings.CacheEnabled {
			cached_endpoint, err := (*cache).Get(c.Request.RequestURI)
			if err == nil {
				log.Info().Msgf("cache hit for page: %s", c.Request.RequestURI)
				c.Data(http.StatusOK, "text/html; charset=utf-8", cached_endpoint.Contents)
				return
			}
		}

		// Before handler call (retrieve from cache)
		html_buffer, err := generator(c, app_settings, db)
		if err != nil {
			log.Error().Msgf("could not generate html: %v", err)
			ErrorHandler(err.Error(), app_settings)(c)
			return
		}

		// After handler  (add to cache)
		if app_settings.CacheEnabled {
			err = (*cache).Store(c.Request.RequestURI, html_buffer)
			if err != nil {
				log.Warn().Msgf("could not add page to cache: %v", err)
			}
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", html_buffer)
	}

	// Hacky
	if method == "GET" {
		e.GET(endpoint, handler)
	}
	if method == "POST" {
		e.POST(endpoint, handler)
	}
	if method == "DELETE" {
		e.DELETE(endpoint, handler)
	}
	if method == "PUT" {
		e.PUT(endpoint, handler)
	}
}

// / This function will act as the handler for
// / the home page
func homeHandler(c *gin.Context, settings common.AppSettings, db database.Database) ([]byte, error) {
	pageNum := 0 // Default to page 0
	if pageNumQuery := c.Param("num"); pageNumQuery != "" {
		num, err := strconv.Atoi(pageNumQuery)
		if err == nil && num > 0 {
			pageNum = num
		} else {
			log.Error().Msgf("Invalid page number: %s", pageNumQuery)
		}
	}
	limit := 10 // or whatever limit you want
	offset := max((pageNum-1)*limit, 0)

	posts, err := db.GetPosts(limit, offset)
	if err != nil {
		return nil, err
	}

	sticky_posts := make([]common.Post, 0)
	for _, sticky_post_id := range settings.StickyPosts {
		post, err := db.GetPost(sticky_post_id)
		if err != nil {
			log.Error().Msgf("could not find sticky post `%d`: %v", sticky_post_id, err)
			continue
		}
		post.Content = string(mdToHTML([]byte(post.Content)))
		sticky_posts = append(sticky_posts, post)
	}

	// if not cached, create the cache
	index_view := views.MakeIndex(posts, sticky_posts, settings.AppNavbar.Links, settings.AppNavbar.Dropdowns)
	html_buffer := bytes.NewBuffer(nil)

	err = index_view.Render(c, html_buffer)
	if err != nil {
		log.Error().Msgf("Could not render index: %v", err)
		return []byte{}, err
	}

	return html_buffer.Bytes(), nil
}
