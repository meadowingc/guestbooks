"""Purge one account from an existing SQLite database with the service stopped.

Includes soft-deleted records. This removes live records, not backup copies or
guaranteed forensic remnants in SQLite pages/WAL. Historical messages whose
guestbook was already hard-deleted cannot be attributed to a username.
"""

import argparse
from pathlib import Path
import sqlite3
import sys


def purge(username, database):
    path = Path(database).resolve()
    if not path.is_file():
        raise ValueError("Database must be an existing file")
    connection = sqlite3.connect(path.as_uri() + "?mode=rw", uri=True)
    try:
        connection.execute("PRAGMA foreign_keys = ON")
        if connection.execute("PRAGMA foreign_keys").fetchone()[0] != 1:
            raise RuntimeError("Cannot enable SQLite foreign-key checks")
        connection.execute("BEGIN IMMEDIATE")
        owner = connection.execute(
            "SELECT id FROM admin_users WHERE username = ?", (username,)
        ).fetchone()
        if owner is None:
            raise ValueError("User not found")
        owner_id = owner[0]
        book_ids = [
            row[0]
            for row in connection.execute(
                "SELECT id FROM guestbooks WHERE admin_user_id = ? ORDER BY id",
                (owner_id,),
            )
        ]
        connection.execute("CREATE TEMP TABLE purge_books (id INTEGER PRIMARY KEY)")
        connection.executemany(
            "INSERT INTO purge_books (id) VALUES (?)",
            ((book_id,) for book_id in book_ids),
        )
        message_count = connection.execute(
            "SELECT COUNT(*) FROM messages WHERE guestbook_id IN (SELECT id FROM purge_books)"
        ).fetchone()[0]
        dangling_count = connection.execute(
            """SELECT COUNT(*) FROM messages AS message
               LEFT JOIN guestbooks AS book ON book.id = message.guestbook_id
               WHERE book.id IS NULL"""
        ).fetchone()[0]
        dangling_books = connection.execute(
            """SELECT COUNT(*) FROM guestbooks AS book
               LEFT JOIN admin_users AS owner ON owner.id = book.admin_user_id
               WHERE owner.id IS NULL"""
        ).fetchone()[0]
        cross_book_children = connection.execute(
            """SELECT COUNT(*) FROM messages AS child
               JOIN messages AS parent ON parent.id = child.parent_message_id
               WHERE parent.guestbook_id IN (SELECT id FROM purge_books)
               AND (child.guestbook_id IS NULL
                    OR child.guestbook_id NOT IN (SELECT id FROM purge_books))"""
        ).fetchone()[0]
        if cross_book_children:
            raise ValueError(
                "Purge blocked: other guestbooks have replies referencing this account; "
                "refusing to delete or cascade into another owner's data"
            )

        print(f"Database: {path}")
        print("Stop the guestbook service before continuing.")
        print(
            f"Account {username!r} (ID {owner_id}): {len(book_ids)} guestbooks, "
            f"{message_count} messages including replies and soft-deleted rows."
        )
        print(
            f"Unattributable messages with missing guestbooks: {dangling_count}. "
            "An older hard purge cannot be reconstructed by username; these are not targeted."
        )
        print(f"Unattributable guestbooks with missing owners: {dangling_books}; these are not targeted.")
        print("Permanent live-record deletion; backups and SQLite/WAL remnants are not erased.")
        if input("Delete this account and its owned records? Type yes: ").strip().lower() != "yes":
            connection.rollback()
            print("Data not deleted")
            return False

        directly_deleted_messages = connection.execute(
            "DELETE FROM messages WHERE guestbook_id IN (SELECT id FROM purge_books)"
        ).rowcount
        deleted_books = connection.execute(
            "DELETE FROM guestbooks WHERE id IN (SELECT id FROM purge_books) AND admin_user_id = ?",
            (owner_id,),
        ).rowcount
        deleted_owners = connection.execute(
            "DELETE FROM admin_users WHERE id = ? AND username = ?", (owner_id, username)
        ).rowcount
        remaining = (
            connection.execute(
                "SELECT COUNT(*) FROM messages WHERE guestbook_id IN (SELECT id FROM purge_books)"
            ).fetchone()[0],
            connection.execute(
                "SELECT COUNT(*) FROM guestbooks WHERE admin_user_id = ? OR id IN (SELECT id FROM purge_books)",
                (owner_id,),
            ).fetchone()[0],
            connection.execute(
                "SELECT COUNT(*) FROM admin_users WHERE id = ?", (owner_id,)
            ).fetchone()[0],
        )
        # SQLite's rowcount excludes self-referential FK cascades; verify all
        # captured rows are gone instead of equating direct deletes with the total.
        if (
            directly_deleted_messages > message_count
            or (message_count > 0 and directly_deleted_messages <= 0)
            or deleted_books != len(book_ids)
            or deleted_owners != 1
            or remaining != (0, 0, 0)
        ):
            raise RuntimeError("Deletion counts did not match; rolling back the entire purge")
        connection.commit()
        print(
            f"Data deleted: {message_count} messages, {deleted_books} guestbooks, "
            f"{deleted_owners} account. Verified remaining counts: messages=0, guestbooks=0, accounts=0."
        )
        return True
    except BaseException:
        connection.rollback()
        raise
    finally:
        connection.close()


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("username")
    parser.add_argument("--database", default="guestbook.db", help="Existing database (default: guestbook.db)")
    args = parser.parse_args(argv)
    if not args.username:
        parser.error("username must not be empty")
    try:
        purge(args.username, args.database)
    except (sqlite3.Error, ValueError, RuntimeError, EOFError) as error:
        print(f"Purge failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
