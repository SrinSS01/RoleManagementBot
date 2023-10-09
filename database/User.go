package database

type User struct {
	UserId  string `db:"userId" json:"userId"`
	GuildId string `db:"guildId" json:"guildId"`
	RoleId  string `db:"roleId" json:"roleId"`
}
