package handlers

import (
	"net/http"
	"snix-surv/models"
	"strconv"

	"github.com/labstack/echo"
)

var (
	users = map[int]*models.User{}
	seq   = 1
)

func UserCreate(c echo.Context) error{
	newUser := &models.User{
		Id: seq,
	}
	if err := c.Bind(newUser); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := c.Validate(newUser); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	users[newUser.Id] = newUser
	seq++
	return c.JSON(http.StatusCreated, newUser)
}

func GetUser(c echo.Context) error {
	id, _ := strconv.Atoi(c.Param("id"))
	return c.JSON(http.StatusOK, users[id])
}