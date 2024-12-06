package routes

import (
	"net/http"
	"snix-surv/handlers"

	"github.com/labstack/echo"
)

func Setup(apiGroup *echo.Group){
	apiGroup.GET("/", func(c echo.Context) error {
		return c.JSON(http.StatusOK, "Hello, World!")
	})
	apiGroup.POST("/users", handlers.UserCreate)

	apiGroup.GET("/users/:id", handlers.GetUser)
}