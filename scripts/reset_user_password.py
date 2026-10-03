#!/usr/bin/env python3
"""
Manual password reset script for when email-based reset isn't working.

Usage:
    python reset_user_password.py --user-id <id>
    python reset_user_password.py --username <username>
    python reset_user_password.py --guestbook-id <guestbook_id>

This will:
1. Look up the user by ID, username, or guestbook ownership
2. Generate a new random password
3. Hash it with bcrypt and update the database
4. Print the new password so you can send it to the user
"""

import sys
import argparse
import sqlite3
import secrets
import string
from pathlib import Path

try:
    import bcrypt
except ImportError:
    print("Error: bcrypt is not installed.")
    print("Install it with: pip install bcrypt")
    sys.exit(1)


def generate_random_password(length=16):
    """Generate a secure random password."""
    alphabet = string.ascii_letters + string.digits
    return ''.join(secrets.choice(alphabet) for _ in range(length))


def resolve_user(cursor, args):
    """Resolve the user from whichever identifier was provided."""
    if args.user_id is not None:
        cursor.execute("SELECT id, username, email FROM admin_users WHERE id=?", (args.user_id,))
        user = cursor.fetchone()
        if not user:
            print(f"Error: User with ID {args.user_id} not found")
            sys.exit(1)
        return user

    if args.username is not None:
        cursor.execute("SELECT id, username, email FROM admin_users WHERE username=?", (args.username,))
        user = cursor.fetchone()
        if not user:
            print(f"Error: User with username '{args.username}' not found")
            sys.exit(1)
        return user

    if args.guestbook_id is not None:
        cursor.execute("SELECT admin_user_id FROM guestbooks WHERE id=?", (args.guestbook_id,))
        row = cursor.fetchone()
        if not row:
            print(f"Error: Guestbook with ID {args.guestbook_id} not found")
            sys.exit(1)
        owner_id = row[0]
        cursor.execute("SELECT id, username, email FROM admin_users WHERE id=?", (owner_id,))
        user = cursor.fetchone()
        if not user:
            print(f"Error: Owner (user ID {owner_id}) of guestbook {args.guestbook_id} not found")
            sys.exit(1)
        return user


def main(args):
    database = Path(args.database).resolve()
    conn = sqlite3.connect(database.as_uri() + "?mode=rw", uri=True)
    cursor = conn.cursor()

    # Look up the user
    user = resolve_user(cursor, args)

    user_id, username, email = user
    print(f"Found user:")
    print(f"  ID:       {user_id}")
    print(f"  Username: {username}")
    print(f"  Email:    {email}")
    print()

    # Confirm before proceeding
    response = input("Do you want to reset this user's password? (yes/no): ")
    if response.lower() != "yes":
        print("Aborted.")
        conn.close()
        sys.exit(0)

    # Generate new password
    new_password = generate_random_password()

    # Hash with bcrypt (this matches the Go code's bcrypt.GenerateFromPassword)
    # Use prefix='2a' to match Go's bcrypt output format exactly
    # (2a and 2b are functionally identical, but this ensures consistency)
    password_hash = bcrypt.hashpw(new_password.encode('utf-8'), bcrypt.gensalt(prefix=b'2a'))

    # Update the database
    # The Go code stores PasswordHash as datatypes.JSON but it's actually just the hash bytes
    cursor.execute(
        "UPDATE admin_users SET password_hash=?, session_token=?, session_expires_at=0, "
        "password_reset_token='', password_reset_expiry=0 WHERE id=? AND deleted_at IS NULL",
        (password_hash.decode('utf-8'), secrets.token_urlsafe(32), user_id)
    )
    if cursor.rowcount != 1:
        conn.rollback()
        conn.close()
        sys.exit("Password reset failed: account no longer exists or is deleted.")
    conn.commit()

    print()
    print("=" * 50)
    print("Password reset successful!")
    print("=" * 50)
    print()
    print(f"New password for user '{username}':")
    print()
    print(f"    {new_password}")
    print()
    print("Please send this password to the user securely.")
    print("They should change it after logging in.")
    print("=" * 50)

    conn.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Reset a user's password manually."
    )
    parser.add_argument("--database", default="guestbook.db", help="Existing SQLite database; stop the service first")
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--user-id", type=int, help="Look up user by their ID")
    group.add_argument("--username", type=str, help="Look up user by their username")
    group.add_argument("--guestbook-id", type=int, help="Look up user by a guestbook they own")

    args = parser.parse_args()
    main(args)
