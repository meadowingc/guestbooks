"""
Will print out the data we know for the user and then prompt if we want to delete the data.

Note that this will do a HARD delete of the data. This means that the data will be gone forever.
Only to be used when someone requests to be purged from the system.
"""

import sys
import sqlite3

def main(username):
    if not username:
        print("Usage: show_all_data_for_user.py <username>")
        sys.exit(1)

    print(f"User: {username}")

    conn = sqlite3.connect('guestbook.db')
    cursor = conn.cursor()

    cursor.execute("SELECT id FROM admin_users WHERE username=?", (username,))
    user_id = cursor.fetchone()

    if not user_id:
        print("User not found")
        sys.exit(1)

    user_id = user_id[0]
    print(f"User ID: {user_id}")


    cursor.execute("SELECT * FROM guestbooks WHERE admin_user_id=?", (user_id,))
    rows = cursor.fetchall()

    print()
    print(f"Found {len(rows)} guestbooks for user {username}")

    column_names = [description[0] for description in cursor.description]

    for row in rows:
        print()
        row_data = {column_names[i]: row[i] for i in range(len(column_names))}
        print(f"Guestbook ID: {row_data['id']}")
        print(f"Guestbook website_url: {row_data['website_url']}")

        # now find all messages for this guestbook
        cursor.execute("SELECT * FROM messages WHERE guestbook_id=?", (row_data['id'],))
        message_rows = cursor.fetchall()

        print(f"\t Found {len(message_rows)} messages for guestbook {row_data['id']}")


    # now ask if user wants to delete all data for this user
    print()
    print("Do you want to delete all data for this user?")
    response = input("yes/no: ")

    if response.lower() == "yes":
        cursor.execute("DELETE FROM admin_users WHERE id=?", (user_id,))
        cursor.execute("DELETE FROM guestbooks WHERE admin_user_id=?", (user_id,))
        cursor.execute("DELETE FROM messages WHERE guestbook_id IN (SELECT id FROM guestbooks WHERE admin_user_id=?)", (user_id,))

        conn.commit()
        print("Data deleted")
    else:
        print("Data not deleted")

    conn.close()

if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("Usage: show_all_data_for_user.py <username>")
        sys.exit(1)

    main(sys.argv[1])