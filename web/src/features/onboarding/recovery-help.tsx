import { useState } from 'react';
import { Button, Dialog } from '../../components/ui';
import { useTranslation } from '../../i18n';

export function RecoveryHelp({ kind }: { kind: 'password' | 'totp' }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const title = t(kind === 'password' ? 'web.auth.forgot_password' : 'web.auth.lost_authenticator');
  return (
    <Dialog open={open} onOpenChange={setOpen} title={title} closeLabel={t('common.close')}
      trigger={<Button variant="plain">{title}</Button>}>
      <div className="space-y-lg">
        <p className="text-body text-text-body">
          {t(kind === 'password' ? 'web.auth.recovery_password' : 'web.auth.recovery_totp')}
        </p>
        <pre className="overflow-x-auto rounded-control bg-inset p-lg text-caption"><code>
          {kind === 'password' ? 'kakatta reset-password --data /data' : 'kakatta disable-totp --data /data'}
        </code></pre>
        {kind === 'password' && <p className="text-body text-text-secondary">{t('web.auth.recovery_disable_totp')}</p>}
      </div>
    </Dialog>
  );
}
