import { CardRegistry } from './card-sdk'
import { builtinCardPlugins } from './builtin-cards'

export const reportCardRegistry = builtinCardPlugins.reduce((registry, plugin) => registry.register(plugin), new CardRegistry())
