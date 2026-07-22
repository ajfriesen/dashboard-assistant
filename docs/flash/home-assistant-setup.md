# Create Home Assistant User

!!! warning
    Create a dedicated Kiosk user.
    Do not use your own user, otherwise everyone with access to the tablet has admin access.
    You have been warned!

Go to users via this link:

[![Open your Home Assistant instance and show your users.](https://my.home-assistant.io/badges/users.svg)](https://my.home-assistant.io/redirect/users/)


Add user:

![alt text](../img/add_user.png)

- give that user local access only
- do not give admin rights

![Fill out user form](../img/fill_out_user_form.png)



## Get Home Assistant Token


Log in as that user in your home assistant instance.
You can use a icognite tab by right click and choose "open in incognito window"

[Open your Home Assistant instance](https://my.home-assistant.io/)

Go to user security for the kiosk/dashboard user:

[![Open your Home Assistant instance and show your Home Assistant user's security options.](https://my.home-assistant.io/badges/profile_security.svg)](https://my.home-assistant.io/redirect/profile_security/)

Generate a long-lived access token:


![Generate a long-lived access token](../img/generate-long-lived-access-token.png)

Give the token a name:
![Give the token a name](../img/name_token.png)
Copy the token to you text editor, password manager.
You need it later.
![Generate a long-lived access to](../img/copy_token.png)


Token looks like this:
```
eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiIyZWRlNGE0ZTFjNmQ0ZDY3OTY4ODhmMTk5OGNhNWVjMSIsImlhdCI6MTc4NDcxODk3MywiZXhwIjoyMTAwMDc4OTczfQ.Rd92pdzdYkC8HI3buVO6m9EVVI71Ye-MP_1nwogfOgU
```