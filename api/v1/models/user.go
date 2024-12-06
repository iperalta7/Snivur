package models

type User struct {
	Name  string `param:"name" query:"name" form:"name" json:"name" xml:"name" validate:"required"`
	Id 	int `param:"id" query:"id" form:"id" json:"id" xml:"id"`
}