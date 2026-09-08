package database

import (
	"fmt"
	"log"
	"time"

	"construct/dev-portal/internal/config"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func Init(cfg *config.Config) {
	var dialector gorm.Dialector

	switch cfg.DBDriver {
	case "postgres":
		sslmode := "disable"
		if cfg.DBSSL == "true" {
			sslmode = "require"
		}
		dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
			cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPass, cfg.DBName, sslmode)
		dialector = postgres.Open(dsn)
	default:
		tls := "false"
		if cfg.DBSSL == "true" {
			tls = "true"
		}
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local&tls=%s",
			cfg.DBUser, cfg.DBPass, cfg.DBHost, cfg.DBPort, cfg.DBName, tls)
		dialector = mysql.Open(dsn)
	}

	var err error
	DB, err = gorm.Open(dialector, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	if sqlDB, err := DB.DB(); err == nil {
		sqlDB.SetMaxOpenConns(50)
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetConnMaxLifetime(30 * time.Minute)
	}

	log.Printf("Connected to %s: %s@%s:%s", cfg.DBDriver, cfg.DBName, cfg.DBHost, cfg.DBPort)
}
