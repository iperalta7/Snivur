package main

import (
	"net/http"
	"os"

	"snix-surv/utils"

	"github.com/labstack/echo"
)

func main () {
	api_version, err := os.ReadFile("VERSION")
	utils.Check(err)
	e := echo.New()

	api_group := e.Group("api/" + string(api_version))
	api_group.GET("/hello", func(c echo.Context) error {
		return c.String(http.StatusOK, "Hello, World!")
	})
	e.Logger.Fatal(e.Start(":8080"))
}