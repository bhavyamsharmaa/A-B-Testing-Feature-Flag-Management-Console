import { createClient } from '@supabase/supabase-js'
import { config } from '../config'

// Publishable key only. Imported after the config check in main.tsx, since
// createClient throws on an empty URL.
export const supabase = createClient(config.supabaseUrl, config.supabasePublishableKey)
