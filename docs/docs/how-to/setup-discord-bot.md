# Set up a Discord bot for HermesAgent

This guide shows you how to create the Discord application, bot user, and token your HermesAgent needs to talk to Discord. The Discord-side steps are one-time per bot and take about five minutes.

You will end up with two things to plug into your CR:

- A **bot token** (a long secret string).
- A bot **invited to your server** with the right scopes.

## 1. Create the application

Go to the [Discord Developer Portal](https://discord.com/developers/applications) and click **New Application**. Give it a name (this is the application name; you can rename the bot separately).

## 2. Create the bot user

In the left nav, open **Bot**. The bot user already exists for new applications. Note the bot's username.

Click **Reset Token** to reveal a new bot token. Copy it now; Discord will not show it again. You will paste this into a Kubernetes Secret in the next section.

## 3. Enable MESSAGE CONTENT INTENT

Still on the **Bot** page, scroll to **Privileged Gateway Intents** and toggle **MESSAGE CONTENT INTENT** on. Save changes.

!!! warning "This is the most common first-boot crashloop"
    Without MESSAGE CONTENT INTENT, the agent crashloops on first boot with `discord.errors.PrivilegedIntentsRequired`. The fix is to come back here and toggle the intent on, then restart the pod.

## 4. Invite the bot to your server

In the left nav, open **OAuth2 → URL Generator**.

- **Scopes**: tick `bot`.
- **Bot Permissions**: at minimum tick `View Channels`, `Send Messages`, `Read Message History`. Add more if you want the agent to be able to (for example) embed images or use slash commands.

Copy the generated URL at the bottom, open it in a browser, pick a server you administer, and authorise. The bot now appears in the member list.

## 5. Capture the bot token in a Secret

```bash
kubectl create namespace hermes
kubectl -n hermes create secret generic my-agent-creds \
    --from-literal=DISCORD_BOT_TOKEN='<the-token-you-copied>' \
    --from-literal=DEEPSEEK_API_KEY='<your-llm-api-key>'
```

Keep the bot token out of any file checked into source control. If it leaks, return to the **Bot** page and click **Reset Token**; the old token stops working immediately.

## 6. Reference the Secret from your HermesAgent

```yaml
spec:
  gateways:
    - type: discord
      env:
        - name: DISCORD_BOT_TOKEN
          valueFrom:
            secretKeyRef: { name: my-agent-creds, key: DISCORD_BOT_TOKEN }
```

Full example: [Your first HermesAgent (Discord)](../tutorials/your-first-hermesagent.md).

## Optional: lock the bot to specific users or channels

Add allowlist env vars on the same gateway entry. Upstream Hermes interprets these:

```yaml
gateways:
  - type: discord
    env:
      - name: DISCORD_BOT_TOKEN
        valueFrom:
          secretKeyRef: { name: my-agent-creds, key: DISCORD_BOT_TOKEN }
      - { name: DISCORD_ALLOWED_USERS, value: "123456789012345678" }
      - { name: DISCORD_ALLOWED_CHANNELS, value: "987654321098765432" }
```

Member IDs and channel IDs are 18-digit snowflakes. Right-click in the Discord client (with Developer Mode enabled) and choose **Copy ID**.

## Verify

After `kubectl apply`-ing the CR:

```bash
kubectl -n hermes logs deployment/hermes-my-agent -c agent --tail=30
```

Look for `Logged in as <bot-name>`. DM the bot from a Discord client; you should get a reply within a few seconds.

## See also

- [Your first HermesAgent (Discord)](../tutorials/your-first-hermesagent.md) — the end-to-end tutorial.
- [Reference: Troubleshooting catalogue](../reference/troubleshooting.md) — `PrivilegedIntentsRequired` and other Discord-related symptoms.
