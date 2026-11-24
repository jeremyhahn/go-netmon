// The e2e suite's default target, the disposable e2e stack; any other target is set with NETMON_URL (checked by scripts/e2e-target-guard.sh).
export const DISPOSABLE_URL = 'http://localhost:8083';

export const BASE = process.env.NETMON_URL || DISPOSABLE_URL;
