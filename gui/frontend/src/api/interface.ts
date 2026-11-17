import type {
  AccountInput, AppSetupChoice, AppSetupEvent, AppSetupStart, BackupExportResult, BackupImportResult, ImportMode, CatalogEntry, DoctorCheck, LogEntry, LoginEvent, RunDone, ServiceInfo, ServiceResult, Settings, Snapshot,
  TestResult, ValidationError,
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
    apply(id: string, choice: AppSetupChoice): Promise<void>;
    cancel(id: string): Promise<void>;
    /** Subscribes to the "appsetup:<id>" event. Returns the unsubscribe function. */
    onEvent(id: string, cb: (e: AppSetupEvent) => void): () => void;
  };
  logs: {
    read(account: string, sinceHours: number, limit: number): Promise<LogEntry[]>;
  };
  settings: {
    get(): Promise<Settings>;
    save(s: Settings): Promise<ValidationError[]>;
    setWebhook(url: string): Promise<void>;
    clearWebhook(): Promise<void>;
    testWebhook(): Promise<void>;
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
    uninstallService(): Promise<ServiceResult>;
  };
  catalog: {
    list(account: string): Promise<CatalogEntry[]>;
    categories(): Promise<string[]>;
  };
  system: {
    openExternal(url: string): Promise<void>;
    /** Subscribes to the Wails event "navigate" ({page}) sent by the backend, e.g. from the tray. Payload is unvalidated. */
    onNavigate(cb: (page: unknown) => void): () => void;
  };
}
