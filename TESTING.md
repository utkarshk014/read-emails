# Testing Push Notifications

## Prerequisites

1. ✅ OAuth authentication is working
2. ✅ `/emails/summary` endpoint is working
3. ⚠️ Pub/Sub topic and subscription must be configured
4. ⚠️ Your ngrok URL must be set as the Pub/Sub push endpoint

## Step-by-Step Testing

### Step 1: Set Project ID

Set the environment variable before starting the server:

```bash
export GOOGLE_CLOUD_PROJECT="read-emails-478721"
```

Or add it to your shell profile (`~/.zshrc` or `~/.bashrc`).

### Step 2: Verify Pub/Sub Configuration

1. **Check Pub/Sub Topic exists:**

   - Go to Google Cloud Console → Pub/Sub → Topics
   - Verify `gmail-notifications` topic exists

2. **Check Pub/Sub Subscription:**

   - Go to Pub/Sub → Subscriptions
   - Verify subscription exists and is configured as **Push**
   - **Endpoint URL must be:** `https://noncounteractive-multiply-carlena.ngrok-free.dev/gmail/push`
   - ⚠️ **Important:** If your ngrok URL changes, update the subscription endpoint!

3. **Verify Gmail Service Account has permissions:**
   - Go to Pub/Sub → Topics → gmail-notifications → Permissions
   - Verify `gmail-api-push@system.gserviceaccount.com` has "Pub/Sub Publisher" role

### Step 3: Start Watch

Call the watch endpoint to start monitoring:

```bash
curl -X POST "https://noncounteractive-multiply-carlena.ngrok-free.dev/watch/start?userEmail=utkarshsuneela@gmail.com"
```

**Expected Response:**

```json
{
  "status": "watch_started",
  "history_id": "1234567890",
  "expiration": "2025-11-21T10:30:00Z"
}
```

**Check server logs** - you should see:

```
Watch started for user utkarshsuneela@gmail.com: topic=projects/read-emails-478721/topics/gmail-notifications, historyId=...
```

### Step 4: Send a Test Email

Send an email to `utkarshsuneela@gmail.com` from another email account (or use Gmail's "Send to self" feature).

### Step 5: Monitor Server Logs

Watch your server logs. When a new email arrives, you should see:

```
Received push notification for user: utkarshsuneela@gmail.com, historyId: ...
New email received for utkarshsuneela@gmail.com:
  Message ID: ...
  Subject: ...
  From: ...
  Date: ...
  Snippet: ...
  Body: ...
```

### Step 6: Verify Webhook Received

Check that Pub/Sub successfully delivered the notification:

- If you see the logs above, the webhook is working! ✅
- If you don't see anything, check:
  1. Is ngrok still running?
  2. Is the Pub/Sub subscription endpoint correct?
  3. Check Pub/Sub subscription metrics in Google Cloud Console

## Troubleshooting

### No push notifications received

1. **Check ngrok is running:**

   ```bash
   # Make sure ngrok is still active
   # If it restarted, you got a new URL - update Pub/Sub subscription!
   ```

2. **Check Pub/Sub subscription:**

   - Go to Pub/Sub → Subscriptions → your-subscription
   - Check "Messages" tab - are there any undelivered messages?
   - Check "Metrics" tab - are messages being delivered?

3. **Verify watch is active:**

   - Watch expires after 7 days
   - Call `/watch/start` again if needed

4. **Check Gmail API quotas:**
   - Go to Google Cloud Console → APIs & Services → Dashboard
   - Check if you've hit any rate limits

### Watch endpoint fails

- Error: "Failed to start watch"
  - Check `GOOGLE_CLOUD_PROJECT` is set correctly
  - Verify Pub/Sub topic exists: `projects/read-emails-478721/topics/gmail-notifications`
  - Verify Gmail service account has publish permissions

### Webhook receives but logs show errors

- Check that the user is authenticated (token exists)
- Verify the historyId parsing is working
- Check Gmail API permissions

## Quick Test Commands

```bash
# 1. Set project ID
export GOOGLE_CLOUD_PROJECT="read-emails-478721"

# 2. Start server
go run main.go

# 3. Start watch (in another terminal)
curl -X POST "http://localhost:8080/watch/start?userEmail=utkarshsuneela@gmail.com"

# 4. Send yourself a test email, then watch server logs
```

## Notes

- **Watch expires after 7 days** - you'll need to call `/watch/start` again
- **ngrok URLs change** - if ngrok restarts, update Pub/Sub subscription endpoint
- **Push notifications are near real-time** - usually arrive within seconds of email delivery
