# Gmail OAuth2 & Push Notifications Server

A single Go HTTP server that handles Google OAuth2 for Gmail, retrieves email summaries, and sets up Gmail push notifications via Google Cloud Pub/Sub.

## Features

- **OAuth2 Authentication**: Secure Google OAuth2 flow for Gmail access
- **Email Summary API**: Get count and latest email from the last 30 days
- **Push Notifications**: Real-time Gmail push notifications via Pub/Sub
- **Webhook Handler**: Receives and processes new email events from Google

## Prerequisites

- Go 1.21 or later
- Google Cloud account
- Gmail account for testing

## Google Cloud Setup

### 2.1 Create and Configure Google Cloud Project

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Create a new project or select an existing one
3. Enable the following APIs:
   - **Gmail API**: [Enable Gmail API](https://console.cloud.google.com/apis/library/gmail.googleapis.com)
   - **Cloud Pub/Sub API**: [Enable Pub/Sub API](https://console.cloud.google.com/apis/library/pubsub.googleapis.com)

### 2.2 OAuth Consent and Credentials

1. Navigate to **APIs & Services → OAuth consent screen**

   - Choose **External** (for public use) or **Internal** (for Google Workspace)
   - Fill in the required app information:
     - App name
     - User support email
     - Developer contact information
   - Add scopes:
     - `https://www.googleapis.com/auth/gmail.readonly` (this is sufficient for all features)
     - **Note:** We use only `gmail.readonly` because `gmail.metadata` doesn't support query parameters (`q` parameter) needed for searching emails
   - **IMPORTANT - Add Test Users:**
     - By default, your app is in "Testing" mode
     - Scroll down to the **Test users** section
     - Click **+ ADD USERS**
     - Add your Gmail address (e.g., `your-email@gmail.com`)
     - Click **ADD**
     - **Only users added here can sign in while the app is in testing mode**
     - If you see "Access blocked" error, it means your email isn't in the test users list

2. Go to **Credentials → Create credentials → OAuth client ID**
   - Choose **Web application** as the application type
   - Set **Authorized redirect URI** to:
     ```
     http://localhost:8080/oauth2/callback
     ```
   - Click **Create**
   - Download the OAuth client JSON file
   - Save it as `credentials.json` in the project root directory

### 2.3 Pub/Sub and Gmail Watch Configuration

**Why Pub/Sub is Required:**

Gmail's push notification system **requires** Google Cloud Pub/Sub. Gmail does NOT directly send webhooks to your server. Instead:

1. **Gmail** publishes email change events to a **Pub/Sub topic**
2. **Pub/Sub** then pushes these events to your webhook endpoint (`/gmail/push`)

This is Google's architecture - Pub/Sub acts as the intermediary between Gmail and your server. It's not optional if you want real-time email notifications.

**Note:** Reading past emails does NOT require Pub/Sub - that's just regular Gmail API calls. Pub/Sub is only needed for receiving webhooks when new emails arrive.

#### Create Pub/Sub Topic

1. Go to **Cloud Pub/Sub → Topics** in the Google Cloud Console
2. Click **Create Topic**
3. Name it `gmail-notifications`
4. Note your **Project ID** (you'll need it later)

#### Grant Permissions to Gmail Service Account

Gmail needs permission to publish messages to your Pub/Sub topic. Grant the Gmail service account the **Pub/Sub Publisher** role:

1. Go to **Cloud Pub/Sub → Topics → gmail-notifications**
2. Click on the **Permissions** tab
3. Click **Add Principal**
4. Add the following service account:
   ```
   gmail-api-push@system.gserviceaccount.com
   ```
5. Assign the role: **Pub/Sub Publisher** (or **Pub/Sub Admin** - both work, but Publisher is more secure)
   - If you don't see "Pub/Sub Publisher" in the dropdown, you can:
     - Search for "publisher" in the role filter
     - Or use **Pub/Sub Admin** (it includes publish permissions)
6. Click **Save**

**Note:** "Pub/Sub Admin" will work fine, but "Pub/Sub Publisher" follows the principle of least privilege (only grants publish permission, not full admin access).

#### Create Push Subscription

1. Go to **Cloud Pub/Sub → Subscriptions**
2. Click **Create Subscription**
3. Configure:
   - **Subscription ID**: `gmail-push-subscription`
   - **Topic**: Select `gmail-notifications`
   - **Delivery type**: Choose **Push**
   - **Endpoint URL**:
     - For local development with ngrok: `https://<your-ngrok-id>.ngrok.io/gmail/push`
     - For production: `https://<your-domain>/gmail/push`
4. Click **Create**

#### Set Environment Variable

Set the `GOOGLE_CLOUD_PROJECT` environment variable to your project ID:

```bash
export GOOGLE_CLOUD_PROJECT="your-project-id"
```

Or add it to your `.env` file if you're using one.

## Required OAuth Scopes

The application uses only one scope:

- `https://www.googleapis.com/auth/gmail.readonly` - **Read email messages and settings**

This single scope provides:

- Full email body content access
- Ability to use query parameters (`q` parameter) for searching emails
- Watch functionality for push notifications

**Why not `gmail.metadata`?** The `gmail.metadata` scope doesn't support the `q` (query) parameter needed for searching emails, so we use only `gmail.readonly` which supports all required features.

## Installation

1. Clone or download this repository

2. Install dependencies:

   ```bash
   go mod download
   ```

3. Place your `credentials.json` file in the project root (see setup instructions above)

4. Set the `GOOGLE_CLOUD_PROJECT` environment variable:
   ```bash
   export GOOGLE_CLOUD_PROJECT="your-project-id"
   ```

## Running the Server

### Local Development

1. Start the server:

   ```bash
   go run main.go
   ```

2. The server will start on `http://localhost:8080`

3. For push notifications to work locally, you'll need to expose your local server using a tool like [ngrok](https://ngrok.com/):
   ```bash
   ngrok http 8080
   ```
   Update your Pub/Sub subscription endpoint to use the ngrok URL.

## API Endpoints

### 1. GET /auth-url

Generates and returns the Google OAuth consent URL.

**Response:**

```json
{
  "auth_url": "https://accounts.google.com/o/oauth2/v2/auth?..."
}
```

**Usage:**

1. Open the returned URL in your browser
2. Complete Google login and consent
3. Google will redirect to `/oauth2/callback`

### 2. GET /oauth2/callback

Handles Google redirect after user approves access. This endpoint:

- Exchanges the authorization code for tokens
- Retrieves the user's email address
- Stores tokens in memory
- Logs authentication details

**Response:** HTML page confirming authentication

### 3. GET /emails/summary?userEmail=<email>

Returns email summary for the last 30 days.

**Query Parameters:**

- `userEmail` (required): The authenticated user's email address

**Response:**

```json
{
  "user_email": "user@example.com",
  "count_last_30_days": 123,
  "latest_email": {
    "id": "18a1b2c3d4e5f6g7",
    "subject": "Hello",
    "from": "Alice <alice@example.com>",
    "date": "Mon, 20 Nov 2025 10:30:00 +0530",
    "snippet": "Just checking in...",
    "body": "Full email body text content here..."
  }
}
```

**Note:** The `body` field contains the full email body text. For multipart messages, it extracts the plain text version when available, or falls back to HTML text.

### 4. POST /watch/start?userEmail=<email>

Sets up Gmail watch for push notifications.

**Query Parameters:**

- `userEmail` (required): The authenticated user's email address

**Response:**

```json
{
  "status": "watch_started",
  "history_id": "1234567890",
  "expiration": "2025-11-21T10:30:00Z"
}
```

**Note:** The watch expires after 7 days. You'll need to call this endpoint again to renew it.

### 5. POST /gmail/push

Webhook endpoint that receives Gmail push notifications from Pub/Sub.

**Request:** Pub/Sub push notification (JSON)

**Response:**

```json
{
  "status": "ok"
}
```

This endpoint:

- Decodes the Pub/Sub message
- Retrieves new email details from Gmail API
- Logs information about new emails

## Usage Flow

1. **Start the server:**

   ```bash
   go run main.go
   ```

2. **Get OAuth URL:**

   ```bash
   curl http://localhost:8080/auth-url
   ```

   Copy the `auth_url` from the response.

3. **Authenticate:**

   - Open the `auth_url` in your browser
   - Complete Google login and consent
   - You'll be redirected to `/oauth2/callback`
   - Check server logs for the authenticated user's email

4. **Get email summary:**

   ```bash
   curl "http://localhost:8080/emails/summary?userEmail=your-email@gmail.com"
   ```

5. **Start watch (for push notifications):**

   ```bash
   curl -X POST "http://localhost:8080/watch/start?userEmail=your-email@gmail.com"
   ```

6. **Test push notifications:**
   - Send an email to the authenticated Gmail account
   - Check server logs for the new email event

## Important Notes

- **Token Storage**: This implementation stores tokens in memory. Tokens will be lost when the server restarts. For production, use persistent storage (database, encrypted file, etc.).

- **History ID**: The server stores the last processed history ID per user. This is used to detect new messages when push notifications arrive.

- **Watch Expiration**: Gmail watches expire after 7 days. You'll need to call `/watch/start` again to renew the watch.

- **Local Development**: For local development, use ngrok or similar tools to expose your local server so Pub/Sub can send push notifications.

- **Project ID**: Make sure to set the `GOOGLE_CLOUD_PROJECT` environment variable to your Google Cloud project ID before starting the watch.

## Troubleshooting

### "User not authenticated" error

- Make sure you've completed the OAuth flow by visiting `/auth-url` and authorizing the application
- Check that you're using the correct email address that was authenticated

### "Failed to start watch" error

- Verify that `GOOGLE_CLOUD_PROJECT` is set correctly
- Check that the Pub/Sub topic exists and Gmail service account has publish permissions
- Ensure the topic name format is correct: `projects/<PROJECT_ID>/topics/gmail-notifications`

### Push notifications not working

- Verify your Pub/Sub subscription is configured as a push subscription
- Check that the endpoint URL is accessible (use ngrok for local development)
- Ensure the subscription endpoint matches your server's `/gmail/push` route
- Check server logs for any errors when processing push notifications

### Token refresh issues

- The OAuth flow requests `offline` access to get a refresh token
- If refresh token is missing, try revoking access and re-authenticating
- Check that `prompt=consent` is included in the auth URL (it is in this implementation)

## References

- [Gmail API Documentation](https://developers.google.com/gmail/api)
- [Gmail API Go Quickstart](https://developers.google.com/gmail/api/quickstart/go)
- [Gmail Push Notifications](https://developers.google.com/gmail/api/guides/push)
- [OAuth 2.0 for Web Server Applications](https://developers.google.com/identity/protocols/oauth2/web-server)
- [Cloud Pub/Sub Documentation](https://cloud.google.com/pubsub/docs)

## License

This project is provided as-is for educational and development purposes.
