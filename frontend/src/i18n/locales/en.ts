// English resource bundle — the default language and the fallback for every
// key a translation is missing. Keep it the authoritative shape: zh-CN.ts
// mirrors these keys.

const en = {
  // The language's own name, for the picker. Never translated: a list of
  // languages labelled in a language you cannot read is no help.
  languageName: 'English',

  common: {
    copy: 'Copy',
    cancel: 'Cancel',
    generate: 'Generate',
    save: 'Save',
    delete: 'Delete',
    prevPage: 'Previous page',
    nextPage: 'Next page',
  },

  nav: {
    publish: 'Publish',
    files: 'My files',
    settings: 'Settings',
    docs: 'Docs',
    admin: 'Admin',
  },

  header: {
    home: 'Placard home',
    openMenu: 'Open navigation menu',
    closeMenu: 'Close navigation menu',
    mobileNav: 'Mobile navigation',
    logout: 'Sign out',
    logoutFailed: 'Sign-out failed, please try again',
    toggleTheme: 'Toggle light and dark theme',
    language: 'Language',
  },

  avatar: {
    alt: 'User avatar',
  },

  clipboard: {
    failed: 'Copy failed — select the text and copy it by hand',
  },

  format: {
    neverExpires: 'Never expires',
    expiresOn: 'Expires {{date}}',
  },

  visibility: {
    private: {
      label: 'Private',
      hint: 'Only you can open it; anyone else following the link sees “page not found”.',
    },
    link: {
      label: 'Link',
      hint: 'Anyone holding the link can open it without signing in — a link that spreads is public.',
    },
  },

  notFound: {
    title: 'Page not found',
    back: 'Back to home',
  },

  dropzone: {
    title: 'Drop an HTML file here',
    hint: 'or <pick>click to choose a file</pick> · .html / .htm · 10MB max',
    choose: 'Choose file',
  },

  publish: {
    titleNew: 'Publish HTML',
    titleUpdate: 'Update page',
    leadNew: 'Upload an <code>.html</code> file and get a link to share.',
    leadUpdate: 'Upload new HTML as the next version of <code>/s/{{id}}</code> — the share link stays the same.',
    titleField: 'Title',
    titlePlaceholder: 'Optional, defaults to the file name',
    expiryLabel: 'Keep for',
    expiry: {
      '1d': '1 day',
      '7d': '7 days',
      '30d': '30 days',
      never: 'Forever',
    },
    visibilityLabel: 'Visibility',
    visibilityDefault: 'Default',
    visibilityDefaultHint: 'Use the default tier from your Settings.',
    uploading: 'Uploading…',
    errorTitle: 'Publishing failed',
    retry: 'Upload again',
    successTitle: 'Published',
    successVersion: 'Published as v{{version}}',
    linkForever: 'Link created, valid forever.',
    linkUntil: 'Link created, expires {{date}}.',
    copyLink: 'Copy link',
    copyLinkAria: 'Copy the share link',
    copied: 'Link copied — go send it to your colleagues',
    continue: 'Upload another',
    openNewTab: 'Open in a new tab ↗',
    publishNew: 'Publish a new page',
    errors: {
      tooLarge: 'The file is over the 10MB limit. Compress it and try again.',
      onlyHTML: 'Only HTML files can be published.',
      rateLimited: 'Too many uploads. Try again shortly.',
      sessionExpired: 'Your session expired. Reload the page.',
      generic: 'Publishing failed, please try again.',
      localType: 'Only .html / .htm files are supported.',
    },
  },

  files: {
    title: 'My files',
    count_one: '{{count}} file in total',
    count_other: '{{count}} files in total',
    loadFailed: 'Could not load the file list',
    copied: 'Link copied',
    deleteConfirm: 'Deleting kills the link immediately and cannot be undone. Delete it?',
    deleted: 'Deleted',
    deleteFailed: 'Delete failed, please try again',
    versionsLoadFailed: 'Could not load the version history',
    pinFollow: 'Now following the latest version',
    pinned: 'Now sharing v{{version}}',
    pinFailed: 'Could not set the shared version',
    restoreConfirm: 'Copy the contents of v{{version}} as a new latest version? History is not rewritten.',
    restored: 'Restored as a new version',
    restoreFailed: 'Restore failed, please try again',
    pager: 'Showing {{start}}–{{end}} of {{total}}',
    empty: {
      title: 'No files yet',
      sub: 'Publish your first HTML page and get a link you can share.',
      cta: 'Publish the first page',
    },
  },

  fileRow: {
    untitled: '(untitled)',
    open: 'Open: {{name}}',
    visibilityTitle: 'Visibility: {{label}}',
    viewsTitle: 'Views (only you can see this)',
    viewsAria_one: '{{count}} view, only you can see this',
    viewsAria_other: '{{count}} views, only you can see this',
    update: 'Update',
    updateAria: 'Update: {{name}}',
    versionsAria: 'Version history: {{name}}',
    pinnedBadge: ' · pinned v{{version}}',
    copyAria: 'Copy link: {{name}}',
    share: 'Share',
    shareAria: 'Share settings: {{name}}',
    deleteAria: 'Delete: {{name}}',
  },

  versions: {
    sharedVersion: 'Shared version',
    followLatest: 'Follow latest (v{{version}})',
    pinTo: 'Pin to v{{version}}',
    untitled: '(untitled)',
    latest: 'Latest',
    restore: 'Restore',
    restoreAria: 'Restore v{{version}} as the latest version',
  },

  share: {
    title: 'Share settings',
    visibility: 'Visibility',
    visibilityUpdated: 'Visibility updated',
    updateFailed: 'Update failed, please try again',
    code: 'Access code',
    codePlaceholder: '——————',
    codeCopied: 'Access code copied',
    regenerate: 'Regenerate',
    clear: 'Clear',
    codeUpdated: 'Access code replaced — the old one stops working immediately',
    codeGenerated: 'Access code created',
    generateFailed: 'Could not create the code, please try again',
    codeCleared: 'Access code cleared',
    clearFailed: 'Could not clear the code, please try again',
    codeHintSet:
      'Visitors must enter this code to open the page, once per page. Regenerating or clearing it locks out everyone already let in.',
    codeHintUnset: 'Once created, anyone holding the link also needs the 6-digit code to open the page.',
    done: 'Done',
  },

  settings: {
    title: 'Settings',
    sub: 'Manage the API tokens used by the CLI and the Skill.',
    tokenCardTitle: 'API Token',
    tokenCardDesc:
      'Used by command-line and Skill calls such as <code>page publish</code>, carried in the <code>Authorization: Bearer</code> header. The plaintext is shown once.',
    createToken: 'Create API Token',
    tokensLoadFailed: 'Could not load your tokens',
    defaultVisibilityTitle: 'Default visibility',
    defaultVisibilityDesc: 'The tier a newly published page gets when it names none of its own.',
    prefsLoadFailed: 'Could not load the default visibility',
    defaultVisibilityUpdated: 'Default visibility updated',
    profileNotReady: 'Your profile is not ready yet — sign in again',
    updateFailed: 'Update failed, please try again',
    revokeConfirm: 'Calls using this token will fail immediately. Revoke it?',
    revoked: 'Token revoked',
    revokeFailed: 'Revoke failed, please try again',
    deleteConfirm: 'The token record cannot be recovered once deleted. Delete it?',
    deleted: 'Token deleted',
    deleteFailed: 'Delete failed, please try again',
    emptyTokens: 'No tokens yet — use “Create API Token” above.',
  },

  tokenRow: {
    unnamed: '(unnamed)',
    neverUsed: 'never used',
    lastUsed: 'last used {{time}}',
    longLived: 'long-lived',
    created: 'created {{time}}',
    revokedSuffix: ' · revoked',
    revoke: 'Revoke',
    revokeAria: 'Revoke: {{name}}',
    deleteAria: 'Delete: {{name}}',
  },

  tokenCreate: {
    title: 'Create API Token',
    name: 'Name',
    namePlaceholder: 'e.g. My laptop / CI pipeline',
    expiry: 'Lifetime',
    expiryOptions: {
      '30d': '30 days',
      '90d': '90 days',
      '365d': '365 days',
    },
    failed: 'Could not create the token, please try again',
  },

  tokenPlaintext: {
    title: 'Token created',
    warning: '⚠ It cannot be shown again once this closes. Copy it now.',
    copyAria: 'Copy the token',
    copied: 'Token copied — keep it safe',
    close: 'I saved it — close',
  },

  identity: {
    title: 'Sign-in methods',
    descWithPassword:
      'Besides your password you can bind single sign-on identities. An account must keep at least one way in.',
    descNoPassword: 'This account has no password; signing in relies entirely on the identities below.',
    loadFailed: 'Could not load your sign-in methods',
    linkedAt: 'Linked · {{date}}',
    unlink: 'Unlink',
    unlinkConfirm: 'You will no longer be able to sign in with {{name}}. Unlink it?',
    unlinked: 'Unlinked {{name}}',
    lastMethod: 'This is the only sign-in method left on the account and cannot be unlinked',
    lastMethodTitle: 'This is the only sign-in method left on the account',
    unlinkFailed: 'Unlink failed, please try again',
    link: 'Link',
  },

  admin: {
    title: 'Admin',
    sub: 'Manage the accounts and settings of this instance.',
    usersLoadFailed: 'Could not load the account list',
    settingsLoadFailed: 'Could not load the instance settings',
    notSelf: 'You cannot do this to your own account',
    actionFailed: 'That action failed, please try again',
    settingsTitle: 'Instance settings',
    settingsDesc:
      'Changes take effect immediately, no restart needed. Database, storage and single sign-on credentials stay in the config file.',
    registrationOpen: 'Open registration',
    registrationOpenHint: 'When closed, only an admin can create accounts.',
    registrationOpened: 'Registration opened',
    registrationClosed: 'Registration closed',
    autoProvision: 'Single sign-on auto-provisioning',
    autoProvisionHint: 'When off, an unlinked single sign-on identity is refused.',
    autoProvisionOn: 'Auto-provisioning enabled',
    autoProvisionOff: 'Auto-provisioning disabled',
    uploadLimit: 'Upload size limit (MB)',
    uploadLimitHint:
      'Lowering it takes effect immediately; raising it past the config file value needs a restart to be honoured.',
    uploadLimitPositive: 'The upload limit must be a positive number',
    uploadLimitUpdated: 'Upload limit updated',
    settingsSaveFailed: 'Could not save the settings, please try again',
    accounts: 'Accounts',
    accountCount_one: '{{count}} account in total',
    accountCount_other: '{{count}} accounts in total',
    disableConfirm: "Disabling ends {{name}}'s sessions and tokens immediately. Disable the account?",
    disabled: 'Disabled {{name}}',
    enabled: 'Enabled {{name}}',
    demoteConfirm: '{{name}} will lose access to the admin page. Remove admin rights?',
    promoted: '{{name}} is now an admin',
    demoted: '{{name}} is no longer an admin',
    passwordPrompt: 'New password for {{name}} (at least 8 characters):',
    passwordTooShort: 'A password needs at least 8 characters',
    passwordReset: "Reset {{name}}'s password",
    passwordRefused: 'The server refused that password — pick another one',
    deleteConfirm: 'Deleting {{name}} also deletes all their pages and cannot be undone. Delete the account?',
    userDeleted: 'Deleted {{name}}',
  },

  adminUserRow: {
    adminBadge: 'Administrator',
    disabledBadge: 'Disabled',
    noEmail: 'no email',
    registered: 'joined {{time}}',
    lastLogin: 'last signed in {{time}}',
    neverLoggedIn: 'never signed in',
    ssoOnly: 'single sign-on only',
    notSelfTitle: 'You cannot do this to your own account',
    enable: 'Enable',
    disable: 'Disable',
    demote: 'Remove admin',
    promote: 'Make admin',
    resetPassword: 'Reset password',
  },

  docs: {
    title: 'Docs',
    lead: 'Placard turns an HTML page into a link you can share — reports, prototypes and visualisations produced by an AI session or a tool, sent to a colleague.',
    installPrompt:
      'Read {{origin}}/install.md and follow it to install and configure the Placard SKILL, then tell me the verification result.',
    skill: {
      title: 'Install the Agent Skill',
      desc: 'Send the prompt below to your AI agent (Claude Code, Codex and friends). It installs the CLI, walks you through signing in, and installs the Placard Skill:',
      copyAria: 'Copy the install prompt',
      copied: 'Install prompt copied',
      links: 'Install guide: <install/>, SKILL reference: <skill/>.',
    },
    publishMethods: {
      title: 'Ways to publish',
      web: '<b>Web upload</b> — drag or pick a <code>.html</code> file on the Publish page and choose how long to keep it.',
      cli: '<b>Command line</b> — the <code>placard</code> CLI is the first choice for a terminal or an AI agent:',
      installLabel: 'Install',
      installCopyAria: 'Copy the install commands',
      installCopied: 'Install commands copied',
      usageLabel: 'Usage',
      usageCopyAria: 'Copy the usage examples',
      usageCopied: 'Usage examples copied',
      api: '<b>HTTP API</b> — call the endpoint directly: create an API token under Settings, then send it in the <code>Authorization: Bearer</code> header:',
      curlCopyAria: 'Copy the curl example',
      curlCopied: 'curl example copied',
      expiry:
        '<code>expiry</code> takes <code>1d / 7d / 30d / never</code>, or any <duration/>; left empty it means forever. Add <update/> to update an existing page. Success returns JSON where <code>url</code> is the share link and <code>version</code> is the version this publish created.',
    },
    cliInstall: {
      loginComment: '# Sign in (Placard has no default server address; name it once and it is remembered)',
    },
    cliUsage: {
      publish: '# Publish a page (creates a share link)',
      list: '# List published pages',
      open: '# Open a page in the browser',
      visibility: '# Change the visibility',
      password: '# Publish with a 6-digit access code (returned once)',
      remove: '# Delete a page',
      update: '# Self-update the CLI',
    },
    curl: {
      title: 'My page',
    },
    visibility: {
      title: 'Visibility',
      link: 'A share link looks like <url/>; pasting it into a chat client shows the page title and description.',
      tiers:
        'Each page has two tiers: <b>Private</b> (only you — anyone else following the link sees “page not found”) and <b>Link</b> (anyone holding the link can read it, no sign-in). Switch it any time from “Share” under My files, or set the default for new pages under Settings.',
      api: 'Publishing through the API accepts a <code>visibility</code> field (<code>private / link</code>); left empty it uses your default tier. The field is ignored when updating an existing page — republishing never changes a published page’s visibility.',
    },
    shareCode: {
      title: 'Access code',
      body: 'The “Share” dialog under My files can give a page a 6-digit access code: whoever holds the link enters it once and is not asked again on that page. Pasted into a chat client, only the title shows — never the page description.',
      rotate:
        'Regenerating or clearing the code locks out every visitor already let in. Publishing with <code>--password auto</code> has the server generate one; it is returned in that one response only, and afterwards can be read from the share dialog.',
    },
    lifecycle: {
      title: 'Managing pages',
      body: 'A publish keeps the page for 1 day / 7 days / 30 days / forever (forever by default); the link dies when it expires. Republishing with the original page id creates a new version and <b>the link stays the same</b>. Under My files you can expand the version history, pin the shared version or restore an old one as the latest, and also copy the link, read the view count or delete the page — after which the link and every version die immediately and cannot be recovered.',
    },
    limits: {
      title: 'Limits',
      fileSize: 'Maximum file size',
      fileSizeValue: '10MB',
      fileType: 'File type',
      fileTypeValue: 'HTML only (.html / .htm)',
      rate: 'Publish rate',
      rateValue: '50 per user per hour',
      tokenTTL: 'Token lifetime',
      tokenTTLValue: '365 days at most',
    },
  },
}

export default en
