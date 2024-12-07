package main

import (
	"fmt"
	"os"
	"path"
	"snix-surv/routes"
	"snix-surv/utils"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo"
	"github.com/labstack/echo/middleware"
)

func main() {
	// use the v1 directory to indicate v1 api
	workingDir, err := os.Getwd()
	utils.Check(err)
	api_version := path.Base(workingDir)
	e := echo.New()
	e.Validator = &utils.CustomValidator{Validator: validator.New()}

	e.Use(middleware.Logger())

	api_group := e.Group("api/" + string(api_version))
	routes.Setup(api_group)

	output := fmt.Sprintf("Server up at localhost:8080/api/%v", api_version)
	e.Logger.Debug(output)
	e.Logger.Fatal(e.Start(":8080"))
}
