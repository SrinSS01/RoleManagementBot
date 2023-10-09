package main

import (
	"RoleManagementBot/config"
	"RoleManagementBot/database"
	"encoding/json"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/labstack/echo/v5"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"log"
	"net/http"
	"os"
)

var (
	discord *discordgo.Session
	cnfg    = config.Config{}
)

func init() {
	file, err := os.ReadFile("config.json")
	if err != nil {
		fmt.Print("Enter bot token: ")
		if _, err := fmt.Scanln(&cnfg.Token); err != nil {
			log.Fatal("Error during Scanln(): ", err)
		}
		configJson()
		return
	}
	if err := json.Unmarshal(file, &cnfg); err != nil {
		log.Fatal("Error during Unmarshal(): ", err)
	}
}

func configJson() {
	marshal, err := json.Marshal(&cnfg)
	if err != nil {
		log.Fatal("Error during Marshal(): ", err)
		return
	}
	if err := os.WriteFile("config.json", marshal, 0644); err != nil {
		log.Fatal("Error during WriteFile(): ", err)
	}
}

func main() {
	app := pocketbase.New()
	app.OnAfterBootstrap().Add(func(e *core.BootstrapEvent) error {
		db := e.App.Dao().DB()
		_, err := db.NewQuery("create table if not exists protected_roles(roleId varchar(20) primary key, guildId varchar(20), color int, name text, isProtected boolean default false);").Execute()
		if err != nil {
			return err
		}
		_, err = db.NewQuery("create table if not exists users(userId varchar(20), guildId varchar(20), roleId varchar(20) references protected_roles(roleId), constraint users_pk primary key (userId, guildId, roleId));").Execute()
		if err != nil {
			return err
		}
		go startBot(db)
		return nil
	})
	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		e.Router.GET("/roles/:guildId", func(c echo.Context) error {
			guildId := c.PathParam("guildId")
			var roles []database.Role
			err := e.App.Dao().DB().NewQuery("select * from protected_roles where guildId = {:guildId}").Bind(dbx.Params{
				"guildId": guildId,
			}).All(&roles)
			if err != nil {
				return err
			}
			return c.JSON(http.StatusOK, roles)
		})
		e.Router.POST("/toggle/:roleId", func(c echo.Context) error {
			roleId := c.PathParam("roleId")
			fmt.Println(roleId)
			_, err := e.App.Dao().DB().NewQuery("update protected_roles set isProtected = not isProtected where roleId = {:roleId}").Bind(dbx.Params{
				"roleId": roleId,
			}).Execute()
			return err
		})
		e.Router.POST("/remove/:user/:guild/:role", func(c echo.Context) error {
			userId := c.PathParam("user")
			guildId := c.PathParam("guild")
			roleId := c.PathParam("role")
			err := deleteMemberFromDatabase(userId, guildId, roleId, e.App.Dao().DB())
			if err != nil {
				return c.JSON(http.StatusExpectationFailed, map[string]string{"message": err.Error()})
			}
			return discord.GuildMemberRoleRemove(guildId, userId, roleId)
		})
		e.Router.POST("/add/:user/:guild/:role", func(c echo.Context) error {
			userId := c.PathParam("user")
			guildId := c.PathParam("guild")
			roleId := c.PathParam("role")
			_, err := e.App.Dao().DB().NewQuery("select * from protected_roles where roleId = {:roleId} and isProtected = true").Bind(dbx.Params{"roleId": roleId}).Execute()
			if err != nil {
				return c.JSON(http.StatusExpectationFailed, map[string]string{"message": err.Error()})
			}
			err = addMemberToDatabase(userId, guildId, roleId, e.App.Dao().DB())
			if err != nil {
				return err
			}
			return discord.GuildMemberRoleAdd(guildId, userId, roleId)
		})
		return nil
	})
	app.OnTerminate().Add(func(e *core.TerminateEvent) error {
		if err := discord.Close(); err != nil {
			return err
		}
		log.Println("Bot is shutting down")
		return nil
	})
	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

func startBot(db dbx.Builder) {
	var err error
	discord, err = discordgo.New("Bot " + cnfg.Token)
	if err != nil {
		log.Fatal("Error creating Discord session", err)
		return
	}
	discord.Identify.Intents |= discordgo.IntentGuilds | discordgo.IntentGuildMembers
	discord.AddHandler(func(s *discordgo.Session, e *discordgo.Ready) {
		log.Println(s.State.User.Username + " is ready")
		for _, guild := range e.Guilds {
			for _, role := range guild.Roles {
				addRoleToDatabase(role, guild.ID, db)
			}
			done := make(chan struct{}, 1)
			rmfn := s.AddHandler(func(s *discordgo.Session, c *discordgo.GuildMembersChunk) {
				if c.ChunkIndex == c.ChunkCount-1 {
					done <- struct{}{}
				}
			})
			defer rmfn()
			err = s.RequestGuildMembers(guild.ID, "", 0, "", false)
			if err != nil {
				fmt.Printf("could not request guild members: %s\n", err.Error())
				return
			}
			<-done
			for _, member := range guild.Members {
				for _, roleId := range member.Roles {
					role := database.Role{}
					err = db.NewQuery("select * from protected_roles where roleId = {:roleId} and isProtected = true").Bind(dbx.Params{
						"roleId": roleId,
					}).One(&role)
					if err != nil {
						continue
					}
					if role.IsProtected {
						user := database.User{}
						err = db.NewQuery("select * from users where userId = {:userId} and guildId = {:guildId} and roleId = {:roleId}").Bind(dbx.Params{
							"userId":  member.User.ID,
							"guildId": guild.ID,
							"roleId":  role.RoleId,
						}).One(&user)
						if err != nil {
							err = s.GuildMemberRoleRemove(guild.ID, member.User.ID, role.RoleId)
							if err != nil {
								fmt.Println(err.Error())
							}
						}
					}
				}
			}
		}
	})
	discord.AddHandler(func(session *discordgo.Session, gc *discordgo.GuildCreate) {
		guild := gc.Guild
		for _, role := range guild.Roles {
			addRoleToDatabase(role, guild.ID, db)
		}
	})
	discord.AddHandler(func(session *discordgo.Session, gc *discordgo.GuildDelete) {
		guild := gc.Guild
		for _, member := range guild.Members {
			_ = deleteMemberFromDatabase(member.User.ID, guild.ID, "", db)
		}
		for _, role := range guild.Roles {
			deleteRoleFromDatabase(role.ID, db)
		}
	})
	discord.AddHandler(func(session *discordgo.Session, gmr *discordgo.GuildMemberRemove) {
		_ = deleteMemberFromDatabase(gmr.Member.User.ID, gmr.GuildID, "", db)
	})
	discord.AddHandler(func(session *discordgo.Session, grc *discordgo.GuildRoleCreate) {
		addRoleToDatabase(grc.Role, grc.GuildID, db)
	})
	discord.AddHandler(func(session *discordgo.Session, grd *discordgo.GuildRoleDelete) {
		deleteRoleFromDatabase(grd.RoleID, db)
	})
	discord.AddHandler(func(session *discordgo.Session, gmu *discordgo.GuildMemberUpdate) {
		member := gmu.Member
		guildId := gmu.GuildID
		for _, roleId := range gmu.Roles {
			role := database.Role{}
			err = db.NewQuery("select * from protected_roles where roleId = {:roleId} and isProtected = true").Bind(dbx.Params{
				"roleId": roleId,
			}).One(&role)
			if err != nil {
				continue
			}
			if role.IsProtected {
				user := database.User{}
				err = db.NewQuery("select * from users where userId = {:userId} and guildId = {:guildId} and roleId = {:roleId}").Bind(dbx.Params{
					"userId":  member.User.ID,
					"guildId": guildId,
					"roleId":  role.RoleId,
				}).One(&user)
				if err != nil {
					err = session.GuildMemberRoleRemove(guildId, member.User.ID, role.RoleId)
					if err != nil {
						fmt.Println(err.Error())
					}
				}
			}
		}
	})
	if err := discord.Open(); err != nil {
		log.Fatal("Error opening connection", err)
		return
	}

	log.Println("Bot is running")
}

func addRoleToDatabase(role *discordgo.Role, guildId string, db dbx.Builder) {
	//fmt.Printf("Adding role ID %s, to database\n", role.ID)
	_, err := db.NewQuery("insert or ignore into protected_roles(roleId, guildId, color, name, isProtected) values ({:roleId}, {:guildId}, {:color}, {:name}, false);").Bind(dbx.Params{
		"roleId":  role.ID,
		"guildId": guildId,
		"color":   role.Color,
		"name":    role.Name,
	}).Execute()
	if err != nil {
		fmt.Println(err.Error())
	}
}

func addMemberToDatabase(member, guild, role string, db dbx.Builder) error {
	fmt.Printf("Adding %s of guild ID: %s, to database\n", member, guild)
	_, err := db.NewQuery("insert or ignore into users(userId, guildId, roleId) values ({:userId}, {:guildId}, {:roleId});").Bind(dbx.Params{
		"userId":  member,
		"guildId": guild,
		"roleId":  role,
	}).Execute()
	return err
}
func deleteRoleFromDatabase(roleId string, db dbx.Builder) {
	fmt.Printf("Deleting role ID %s, from database\n", roleId)
	_, err := db.NewQuery("delete from protected_roles where roleId = {:roleId};").Bind(dbx.Params{
		"roleId": roleId,
	}).Execute()
	if err != nil {
		fmt.Println(err.Error())
	}
}

func deleteMemberFromDatabase(member, guild, role string, db dbx.Builder) error {
	fmt.Printf("Deleting %s of guild ID: %s, from database\n", member, guild)
	if role == "" {
		_, err := db.NewQuery("delete from users where userId = {:userId} and guildId = {:guildId};").Bind(dbx.Params{
			"userId":  member,
			"guildId": guild,
		}).Execute()
		if err != nil {
			return err
		}
	}
	_, err := db.NewQuery("delete from users where userId = {:userId} and guildId = {:guildId} and roleId = {:roleId};").Bind(dbx.Params{
		"userId":  member,
		"guildId": guild,
		"roleId":  role,
	}).Execute()
	if err != nil {
		return err
	}
}
