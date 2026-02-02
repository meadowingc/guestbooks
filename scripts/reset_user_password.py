#!/usr/bin/env python3
"""
Manual password reset script for when email-based reset isn't working.

Usage: python reset_user_password.py <user_id>

This will:
1. Look up the user by ID
2. Generate a new random password
3. Hash it with bcrypt and update the database
4. Print the new password so you can send it to the user
"""

import sys
import sqlite3
import secrets
import string

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


def main(user_id):
    if not user_id:
        print("Usage: reset_user_password.py <user_id>")
        sys.exit(1)

    try:
        user_id = int(user_id)
    except ValueError:
        print(f"Error: user_id must be an integer, got: {user_id}")
        sys.exit(1)

    conn = sqlite3.connect('guestbook.db')
    cursor = conn.cursor()

    # Look up the user
    cursor.execute("SELECT id, username, email FROM admin_users WHERE id=?", (user_id,))
    user = cursor.fetchone()

    if not user:
        print(f"Error: User with ID {user_id} not found")
        conn.close()
        sys.exit(1)

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
        "UPDATE admin_users SET password_hash=? WHERE id=?",
        (password_hash.decode('utf-8'), user_id)
    )
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
    if len(sys.argv) != 2:
        print("Usage: reset_user_password.py <user_id>")
        print()
        print("Example: python reset_user_password.py 42")
        sys.exit(1)

    main(sys.argv[1])
