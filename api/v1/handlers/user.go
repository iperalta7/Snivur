package handlers

import (
	"net/http"
	"snix-surv/models"

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
		return err
	}
	users[newUser.Id] = newUser
	seq++
	return c.JSON(http.StatusCreated, newUser)
}