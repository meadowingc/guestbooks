import sqlite3
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import bcrypt


class PasswordResetTests(unittest.TestCase):
    def test_reset_revokes_session_and_recovery_without_touching_other_accounts(self):
        with tempfile.TemporaryDirectory() as directory:
            database = Path(directory) / "fixture.db"
            with sqlite3.connect(database) as connection:
                connection.execute(
                    "CREATE TABLE admin_users (id INTEGER PRIMARY KEY, username TEXT, "
                    "email TEXT, password_hash TEXT, session_token TEXT UNIQUE, "
                    "session_expires_at INTEGER, password_reset_token TEXT, "
                    "password_reset_expiry INTEGER, deleted_at TEXT)"
                )
                for identifier in (1, 2):
                    connection.execute(
                        "INSERT INTO admin_users VALUES (?, ?, '', 'old-hash', ?, 9999999999, ?, 9999999999, NULL)",
                        (identifier, f"user-{identifier}", f"session-{identifier}", f"reset-{identifier}"),
                    )
            script = Path(__file__).with_name("reset_user_password.py")
            response = subprocess.run(
                [sys.executable, str(script), "--username", "user-1", "--database", str(database)],
                input="yes\n", text=True, capture_output=True, check=True,
            )
            self.assertIn("Password reset successful", response.stdout)
            with sqlite3.connect(database) as connection:
                first = connection.execute(
                    "SELECT password_hash, session_token, session_expires_at, "
                    "password_reset_token, password_reset_expiry FROM admin_users WHERE id=1"
                ).fetchone()
                second = connection.execute(
                    "SELECT password_hash, session_token FROM admin_users WHERE id=2"
                ).fetchone()
            self.assertNotEqual(first[0], "old-hash")
            self.assertTrue(first[0].startswith("$2a$"))
            self.assertNotEqual(first[1], "session-1")
            self.assertEqual(first[2:], (0, "", 0))
            self.assertEqual(second, ("old-hash", "session-2"))
            generated = response.stdout.split("New password for user 'user-1':", 1)[1].splitlines()
            password = next(line.strip() for line in generated if line.strip())
            self.assertTrue(bcrypt.checkpw(password.encode(), first[0].encode()))

    def test_missing_database_is_not_created(self):
        with tempfile.TemporaryDirectory() as directory:
            database = Path(directory) / "missing.db"
            response = subprocess.run(
                [sys.executable, str(Path(__file__).with_name("reset_user_password.py")),
                 "--username", "missing", "--database", str(database)],
                input="yes\n", text=True, capture_output=True,
            )
            self.assertNotEqual(response.returncode, 0)
            self.assertFalse(database.exists())


if __name__ == "__main__":
    unittest.main()
