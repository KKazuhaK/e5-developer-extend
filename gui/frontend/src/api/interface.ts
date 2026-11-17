import type {
  APIStat, AccountInput, AppSetupCancelResult, AppSetupChoice, AppSetupEvent, AppSetupPlan, AppSetupStart, BackupExportResult, BackupImportResult, ImportMode, CatalogEntry, DoctorCheck, LogEntry, LoginEvent, RunDone, ServiceInfo, ServiceResult, Settings, Snapshot,
  RecommendResult, SubscriptionRefresh, TenantSubscriptions, TestResult, ValidationError, ChannelInput, ChannelTest, ChannelType, ChannelView, EmailPreset, MessagePreview,
} from "./types";

/**
 * What the UI needs from the backend. One method per contract method
 * (docs/design/gui.md section 3), grouped by Wails service. Every method
 * rejects with an Error whose message includes the fix hint from ops.
 */
export interface Api {
  status: {
    snapshot(): Promise<Snapshot>;
    /** With no engine running the round runs in-process and RunNow returns at once; onRunDone reports the end. */
    runNow(names: string[]): Promise<void>;
    /** Subscribes to the "run:done" event. Returns the unsubscribe function. */
    onRunDone(cb: (e: RunDone) => void): () => void;
  };
  accounts: {
    add(input: AccountInput): Promise<void>;
    update(input: AccountInput): Promise<void>;
    remove(name: string): Promise<void>;
    setSecret(name: string, secret: string): Promise<void>;
    /** Starts a login; progress and result arrive via onLoginEvent. */
    login(name: string): Promise<void>;
    /** Login for an account whose app registration was just created: waits while Microsoft replicates it ("progress" events). */
    loginAfterSetup(name: string): Promise<void>;
    cancelLogin(name: string): Promise<void>;
    test(name: string): Promise<TestResult>;
    /** Probes (read-only) which API categories a signed-in account can really use; needsLogin when it is not signed in. */
    recommend(name: string): Promise<RecommendResult>;
    pause(name: string): Promise<void>;
    resume(name: string): Promise<void>;
    get(name: string): Promise<AccountInput>;
    /** Subscribes to the "login:<name>" event. Returns the unsubscribe function. */
    onLoginEvent(name: string, cb: (e: LoginEvent) => void): () => void;
  };
  /**
   * Automatic app registration. The caller picks the session id (letters, digits, - and _, 8 to 64)
   * so it can subscribe before begin(). Progress and results arrive via onEvent; any failure ends
   * the session with an "error" event. Nothing in them is a token or a secret.
   */
  appSetup: {
    begin(id: string, start: AppSetupStart): Promise<void>;
    /**
     * Repairs an existing account: adds the missing permissions to the app registration the account signs in
     * with (no new account, no new client secret; any other app is refused). Same events as begin; the caller
     * ends it with finish after "result".
     */
    repair(id: string, account: string, method: "device_code" | "browser"): Promise<void>;
    /** The plan as it would be for this choice (actions and missing permissions); changes nothing. */
    preview(id: string, choice: AppSetupChoice): Promise<AppSetupPlan>;
    apply(id: string, choice: AppSetupChoice): Promise<void>;
    /** Ends the session after the account was saved. */
    finish(id: string): Promise<void>;
    /** Abandons the session; after a finished app-only setup that nobody uses it also deletes the new client secret. */
    cancel(id: string): Promise<AppSetupCancelResult>;
    /** Subscribes to the "appsetup:<id>" event. Returns the unsubscribe function. */
    onEvent(id: string, cb: (e: AppSetupEvent) => void): () => void;
  };
  logs: {
    read(account: string, sinceHours: number, limit: number): Promise<LogEntry[]>;
    /** Per-API call counters of one account ("" adds up all accounts); only APIs that were called. */
    apiStats(account: string): Promise<APIStat[]>;
  };
  settings: {
    get(): Promise<Settings>;
    save(s: Settings): Promise<ValidationError[]>;
    /** Turns the tray's login entry on or off at once; not part of save. */
    setStartAtLogin(on: boolean): Promise<void>;
    /** The notification channels; secrets are only reported as set or not set. */
    listChannels(): Promise<ChannelView[]>;
    /** Creates (create true) or updates a channel; returns the problems, one per field, when it is not valid. */
    saveChannel(c: ChannelInput, create: boolean): Promise<ValidationError[]>;
    removeChannel(id: string): Promise<void>;
    /** Sends a test message through one channel, or all of them when id is empty. */
    testChannel(id: string): Promise<ChannelTest[]>;
    /** Renders an example event with an optional custom template; sends nothing. Rejects when the template is broken. */
    previewMessage(type: ChannelType, language: string, template: string, format: string, event: string): Promise<MessagePreview>;
    emailPresets(): Promise<EmailPreset[]>;
  };
  backup: {
    /** True when this machine already has accounts or vault data, so an import must merge or replace. */
    targetHasData(): Promise<boolean>;
    /** Asks for a file (system dialog, titled dialogTitle) and writes the backup there. */
    export(passphrase: string, withLogs: boolean, dialogTitle: string): Promise<BackupExportResult>;
    /** Asks for a backup file (system dialog) and restores it. */
    import(passphrase: string, mode: ImportMode, dialogTitle: string): Promise<BackupImportResult>;
  };
  health: {
    doctor(offline: boolean): Promise<DoctorCheck[]>;
    serviceInfo(): Promise<ServiceInfo>;
    installService(): Promise<ServiceResult>;
    /** Starts the installed service when it is stopped; does not rewrite it. */
    startService(): Promise<ServiceResult>;
    uninstallService(): Promise<ServiceResult>;
  };
  subscription: {
    /** What the engine last saw of each tenant's subscriptions; makes no network call. */
    list(): Promise<TenantSubscriptions[]>;
    /** Reads them now for the tenant of the account (every tenant when empty); a running engine is asked and waited for. */
    refresh(account: string): Promise<SubscriptionRefresh>;
    /** Writes the end date (YYYY-MM-DD, "" removes it) of the account's tenant, for when it cannot be read. */
    setExpires(account: string, date: string): Promise<void>;
  };
  catalog: {
    list(account: string): Promise<CatalogEntry[]>;
    categories(): Promise<string[]>;
    /** The categories an account without a categories setting runs on. */
    defaultCategories(): Promise<string[]>;
  };
  system: {
    openExternal(url: string): Promise<void>;
    /** Subscribes to the Wails event "navigate" ({page}) sent by the backend, e.g. from the tray. Payload is unvalidated. */
    onNavigate(cb: (page: unknown) => void): () => void;
  };
}
