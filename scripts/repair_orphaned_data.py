"""Report orphaned guestbooks/messages; default is a read-only dry run.

Stop the service, back up the database, then use --apply and confirm to soft-delete
active descendants of missing/deleted owners, guestbooks, or parents. No records
are permanently deleted. Already-hard-purged ownership cannot be reconstructed.
"""

import argparse
from pathlib import Path
import sqlite3
import sys


def repair(database, apply=False):
    path = Path(database).resolve()
    if not path.is_file():
        raise ValueError("Database must be an existing file")
    mode = "rw" if apply else "ro"
    connection = sqlite3.connect(path.as_uri() + f"?mode={mode}", uri=True)
    try:
        connection.execute("BEGIN IMMEDIATE" if apply else "BEGIN")
        books = [
            row[0]
            for row in connection.execute(
                """SELECT book.id FROM guestbooks AS book
                   LEFT JOIN admin_users AS owner ON owner.id = book.admin_user_id
                   WHERE book.deleted_at IS NULL
                   AND (owner.id IS NULL OR owner.deleted_at IS NOT NULL)
                   ORDER BY book.id"""
            )
        ]
        messages = [
            row[0]
            for row in connection.execute(
                """WITH RECURSIVE invalid_messages(id) AS (
                       SELECT message.id FROM messages AS message
                       LEFT JOIN guestbooks AS book ON book.id = message.guestbook_id
                       LEFT JOIN admin_users AS owner ON owner.id = book.admin_user_id
                       LEFT JOIN messages AS parent ON parent.id = message.parent_message_id
                       WHERE message.deleted_at IS NULL AND (
                           book.id IS NULL OR book.deleted_at IS NOT NULL
                           OR owner.id IS NULL OR owner.deleted_at IS NOT NULL
                           OR (message.parent_message_id IS NOT NULL AND (
                               parent.id IS NULL OR parent.deleted_at IS NOT NULL
                               OR parent.guestbook_id IS NOT message.guestbook_id
                           ))
                       )
                       UNION
                       SELECT child.id FROM messages AS child
                       JOIN invalid_messages AS parent ON child.parent_message_id = parent.id
                       WHERE child.deleted_at IS NULL
                   )
                   SELECT id FROM invalid_messages ORDER BY id"""
            )
        ]
        dangling_messages = connection.execute(
            """SELECT COUNT(*) FROM messages AS message
               LEFT JOIN guestbooks AS book ON book.id = message.guestbook_id
               WHERE book.id IS NULL"""
        ).fetchone()[0]
        dangling_books = connection.execute(
            """SELECT COUNT(*) FROM guestbooks AS book
               LEFT JOIN admin_users AS owner ON owner.id = book.admin_user_id
               WHERE owner.id IS NULL"""
        ).fetchone()[0]
        print(f"Database: {path}")
        print(f"{'APPLY preview' if apply else 'DRY RUN'}: {len(books)} active guestbooks, {len(messages)} active messages need soft deletion.")
        print(
            f"Unattributable historical rows (including already deleted): {dangling_messages} messages "
            f"with missing guestbooks; {dangling_books} guestbooks with missing owners. "
            "Their original usernames cannot be reconstructed."
        )
        if not apply:
            connection.rollback()
            print("No data changed. Stop the service, back up, and rerun with --apply to confirm soft deletion.")
            return len(books), len(messages)
        if input("Service stopped and backup made? Type yes to soft-delete only these rows: ").strip().lower() != "yes":
            connection.rollback()
            print("No data changed")
            return 0, 0
        timestamp = connection.execute("SELECT strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')").fetchone()[0]
        for table, ids in (("guestbooks", books), ("messages", messages)):
            for record_id in ids:
                result = connection.execute(
                    f"UPDATE {table} SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL",
                    (timestamp, record_id),
                )
                if result.rowcount != 1:
                    raise RuntimeError(f"{table} row count changed; rolling back repair")
            for record_id in ids:
                remaining = connection.execute(
                    f"SELECT COUNT(*) FROM {table} WHERE id = ? AND deleted_at IS NULL",
                    (record_id,),
                ).fetchone()[0]
                if remaining:
                    raise RuntimeError(f"{table} verification failed; rolling back repair")
        connection.commit()
        print(f"Soft-deleted {len(books)} guestbooks and {len(messages)} messages; verified zero selected active rows. No permanent deletion.")
        return len(books), len(messages)
    except BaseException:
        connection.rollback()
        raise
    finally:
        connection.close()


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--database", default="guestbook.db", help="Existing database (default: guestbook.db)")
    parser.add_argument("--apply", action="store_true", help="Require confirmation, then soft-delete orphaned active rows")
    args = parser.parse_args(argv)
    try:
        repair(args.database, args.apply)
    except (sqlite3.Error, ValueError, RuntimeError, EOFError) as error:
        print(f"Repair failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
