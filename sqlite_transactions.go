package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

// Reserve the SQLite writer before reading; a deferred WAL snapshot cannot
// become writable after another connection commits. Read-only API transactions
// remain deferred so they do not block writers.
func writeTransaction(database *gorm.DB, operation func(*gorm.DB) error) error {
	return database.Connection(func(connection *gorm.DB) (err error) {
		sqlConnection, ok := connection.Statement.ConnPool.(*sql.Conn)
		if !ok {
			return errors.New("SQLite write transaction requires a dedicated SQL connection")
		}
		discardConnection := func() error {
			discardErr := sqlConnection.Raw(func(any) error { return driver.ErrBadConn })
			if errors.Is(discardErr, driver.ErrBadConn) || errors.Is(discardErr, sql.ErrConnDone) {
				return nil
			}
			return discardErr
		}
		tx := connection.Session(&gorm.Session{SkipDefaultTransaction: true})
		if err = tx.Exec("BEGIN IMMEDIATE").Error; err != nil {
			// A canceled BEGIN can have an uncertain result; never reuse that connection.
			return errors.Join(err, discardConnection())
		}
		committed := false
		defer func() {
			if committed {
				return
			}
			// Request cancellation must not leave a transaction on a pooled connection.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(tx.Statement.Context), 5*time.Second)
			defer cancel()
			if rollbackErr := tx.WithContext(ctx).Exec("ROLLBACK").Error; rollbackErr != nil {
				cleanupErr := errors.Join(fmt.Errorf("rollback write transaction: %w", rollbackErr), discardConnection())
				log.Printf("Rollback SQLite write transaction: %v", cleanupErr)
				err = errors.Join(err, cleanupErr)
			}
		}()
		if err = operation(tx); err != nil {
			return err
		}
		if err = tx.Exec("COMMIT").Error; err != nil {
			return err
		}
		committed = true
		return nil
	})
}
