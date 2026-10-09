import { useState } from 'react'
import { AccessGate } from '../components/AccessGate'
import { ConsoleCard } from '../components/ConsoleCard'
import { ConsoleHeader } from '../components/ConsoleHeader'
import { InvitesSection } from '../components/settings/InvitesSection'
import { MembersSection } from '../components/settings/MembersSection'
import { SdkKeysSection } from '../components/settings/SdkKeysSection'
import { WorkspaceSection } from '../components/settings/WorkspaceSection'
import { useConsoleEnv } from '../lib/useConsoleEnv'

export default function SettingsPage() {
  const { email, me, meLoading, meError, reloadMe, signOut, roles, workspace } = useConsoleEnv()
  // Bumped when an invite is created so the members list refreshes alongside it.
  const [membersKey, setMembersKey] = useState(0)

  return (
    <main className="mx-auto max-w-5xl px-4 py-10">
      <ConsoleHeader email={email} onSignOut={() => void signOut()} />

      <ConsoleCard prod={false}>
        <AccessGate me={me} meLoading={meLoading} meError={meError} reloadMe={reloadMe} roleCount={roles.length} />

        {workspace && (
          <div className="space-y-9">
            <div>
              <h2 className="text-xs font-medium uppercase tracking-wider text-zinc-500">
                Settings for <span className="text-zinc-300">{workspace.name}</span>
              </h2>
            </div>
            <WorkspaceSection key={workspace.id + workspace.name} workspace={workspace} />
            <MembersSection workspace={workspace} reloadKey={membersKey} />
            <InvitesSection workspace={workspace} onChanged={() => setMembersKey((k) => k + 1)} />
            <SdkKeysSection workspace={workspace} />
          </div>
        )}
      </ConsoleCard>
    </main>
  )
}
