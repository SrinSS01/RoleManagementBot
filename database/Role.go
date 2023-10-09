package database

type Role struct {
	RoleId      string `db:"roleId" json:"roleId"`
	GuildId     string `db:"guildId" json:"guildId"`
	Color       int    `db:"color" json:"color"`
	Name        string `db:"name" json:"name"`
	IsProtected bool   `db:"isProtected" json:"isProtected"`
}
