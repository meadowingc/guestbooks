"""Synthetic-only regression tests for stopped-service maintenance commands."""

from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest


class DataMaintenanceTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="guestbooks-maintenance-test-")
        self.addCleanup(self.directory.cleanup)
        self.database = Path(self.directory.name) / "synthetic.sqlite"
        with sqlite3.connect(self.database) as connection:
            connection.executescript(
                """
                CREATE TABLE admin_users (
                    id INTEGER PRIMARY KEY, username TEXT UNIQUE, deleted_at TEXT
                );
                CREATE TABLE guestbooks (
                    id INTEGER PRIMARY KEY,
                    admin_user_id INTEGER REFERENCES admin_users(id) ON DELETE CASCADE,
                    deleted_at TEXT
                );
                CREATE TABLE messages (
                    id INTEGER PRIMARY KEY,
                    guestbook_id INTEGER REFERENCES guestbooks(id) ON DELETE CASCADE,
                    parent_message_id INTEGER REFERENCES messages(id) ON DELETE CASCADE,
                    email TEXT, deleted_at TEXT
                );
                INSERT INTO admin_users VALUES (1, 'target', NULL), (2, 'keep', NULL), (3, 'deleted', '2020-01-01');
                INSERT INTO guestbooks VALUES (10, 1, NULL), (11, 1, '2020-01-01'), (20, 2, NULL);
                INSERT INTO messages VALUES
                    (101, 10, NULL, 'target-root@example.test', NULL),
                    (102, 10, 101, 'target-reply@example.test', NULL),
                    (103, 10, 101, 'target-deleted-reply@example.test', '2020-01-01'),
                    (110, 11, NULL, 'target-deleted-book@example.test', NULL),
                    (111, 11, 110, 'target-deleted-both@example.test', '2020-01-01'),
                    (201, 20, NULL, 'keep-root@example.test', NULL),
                    (202, 20, 201, 'keep-reply@example.test', NULL);
                """
            )

    def run_script(self, name, *arguments, answer="yes\n", database=None):
        return subprocess.run(
            [
                sys.executable,
                str(Path(__file__).with_name(name)),
                *arguments,
                "--database",
                str(database or self.database),
            ],
            input=answer,
            text=True,
            capture_output=True,
            timeout=10,
            check=False,
        )

    def rows(self):
        with sqlite3.connect(self.database) as connection:
            return {
                table: connection.execute(f"SELECT * FROM {table} ORDER BY id").fetchall()
                for table in ("admin_users", "guestbooks", "messages")
            }

    def test_purge_includes_soft_deleted_rows_and_preserves_other_owners(self):
        with sqlite3.connect(self.database) as connection:
            connection.execute("UPDATE admin_users SET deleted_at = '2020-01-01' WHERE id = 1")
        before = self.rows()
        result = self.run_script("hard_delete_all_data_for_username.py", "target")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("5 messages, 2 guestbooks, 1 account", result.stdout)
        self.assertIn("messages=0, guestbooks=0, accounts=0", result.stdout)
        self.assertNotIn("target-root@example.test", result.stdout)
        after = self.rows()
        self.assertEqual(after["messages"], [row for row in before["messages"] if row[1] == 20])
        self.assertEqual(after["guestbooks"], [row for row in before["guestbooks"] if row[1] == 2])
        self.assertEqual(after["admin_users"], [row for row in before["admin_users"] if row[0] != 1])

    def test_purge_refusal_and_failure_are_atomic(self):
        before = self.rows()
        result = self.run_script("hard_delete_all_data_for_username.py", "target", answer="no\n")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.rows(), before)
        with sqlite3.connect(self.database) as connection:
            connection.execute(
                """CREATE TRIGGER reject_purge BEFORE DELETE ON guestbooks
                   WHEN OLD.id = 10 BEGIN SELECT RAISE(ABORT, 'injected failure'); END"""
            )
        result = self.run_script("hard_delete_all_data_for_username.py", "target")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Purge failed", result.stderr)
        self.assertNotIn("Data deleted:", result.stdout)
        self.assertEqual(self.rows(), before)

    def test_purge_rejects_cross_owner_cascades(self):
        with sqlite3.connect(self.database) as connection:
            connection.execute(
                "INSERT INTO messages VALUES (203, 20, 101, 'keep-cross-book@example.test', NULL)"
            )
        before = self.rows()
        result = self.run_script("hard_delete_all_data_for_username.py", "target")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("another owner's data", result.stderr)
        self.assertEqual(self.rows(), before)

    def test_missing_database_is_never_created(self):
        missing = Path(self.directory.name) / "does-not-exist.sqlite"
        for name, arguments in (
            ("hard_delete_all_data_for_username.py", ["target"]),
            ("repair_orphaned_data.py", []),
            ("repair_orphaned_data.py", ["--apply"]),
        ):
            with self.subTest(script=name, arguments=arguments):
                result = self.run_script(name, *arguments, database=missing)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(missing.exists())

    def add_orphans(self):
        with sqlite3.connect(self.database) as connection:
            connection.executescript(
                """
                INSERT INTO guestbooks VALUES (30, 3, NULL), (40, 404, NULL);
                INSERT INTO messages VALUES
                    (301, 30, NULL, 'deleted-owner@example.test', NULL),
                    (401, 40, NULL, 'missing-owner@example.test', NULL),
                    (501, 999, NULL, 'missing-book@example.test', NULL),
                    (601, 20, 999999, 'missing-parent@example.test', NULL),
                    (602, 20, 601, 'deep-reply@example.test', NULL),
                    (203, 20, NULL, 'deleted-parent@example.test', '2020-01-01'),
                    (603, 20, 203, 'deleted-parent-reply@example.test', NULL),
                    (604, 20, 101, 'cross-book-parent@example.test', NULL);
                """
            )

    def test_repair_dry_run_confirmation_and_transitive_soft_deletion(self):
        self.add_orphans()
        before = self.rows()
        result = self.run_script("repair_orphaned_data.py")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("DRY RUN: 2 active guestbooks, 8 active messages", result.stdout)
        self.assertIn("original usernames cannot be reconstructed", result.stdout)
        self.assertEqual(self.rows(), before)
        result = self.run_script("repair_orphaned_data.py", "--apply", answer="no\n")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.rows(), before)
        result = self.run_script("repair_orphaned_data.py", "--apply")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("No permanent deletion", result.stdout)
        after = self.rows()
        self.assertEqual(after["admin_users"], before["admin_users"])
        self.assertEqual(len(after["messages"]), len(before["messages"]))
        changed_messages = {110, 301, 401, 501, 601, 602, 603, 604}
        for previous, current in zip(before["messages"], after["messages"]):
            self.assertEqual(current[:4], previous[:4])
            if current[0] in changed_messages:
                self.assertIsNotNone(current[4])
            else:
                self.assertEqual(current, previous)
        for previous, current in zip(before["guestbooks"], after["guestbooks"]):
            if current[0] in {30, 40}:
                self.assertIsNotNone(current[2])
            else:
                self.assertEqual(current, previous)
        result = self.run_script("repair_orphaned_data.py", "--apply")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Soft-deleted 0 guestbooks and 0 messages", result.stdout)

    def test_repair_failure_rolls_back_books_and_messages(self):
        self.add_orphans()
        before = self.rows()
        with sqlite3.connect(self.database) as connection:
            connection.execute(
                """CREATE TRIGGER reject_repair BEFORE UPDATE OF deleted_at ON messages
                   WHEN NEW.id = 603 BEGIN SELECT RAISE(ABORT, 'injected failure'); END"""
            )
        result = self.run_script("repair_orphaned_data.py", "--apply")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Repair failed", result.stderr)
        self.assertEqual(self.rows(), before)

    def test_purge_reports_unattributable_rows_without_claiming_ownership(self):
        with sqlite3.connect(self.database) as connection:
            connection.execute(
                "INSERT INTO messages VALUES (501, 999, NULL, 'unattributable@example.test', NULL)"
            )
        result = self.run_script("hard_delete_all_data_for_username.py", "target")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Unattributable messages with missing guestbooks: 1", result.stdout)
        self.assertIn((501, 999, None, "unattributable@example.test", None), self.rows()["messages"])


if __name__ == "__main__":
    unittest.main()
