const { InstanceBase, InstanceStatus, runEntrypoint, combineRgb } = require('@companion-module/base')

class DanteStreamerInstance extends InstanceBase {
	constructor(internal) {
		super(internal)
		this.sounds = []
		this.zones = []
		this.soundboardState = { enabled: false, sounds: [], playing: [] }
		this.pollTimer = null
	}

	async init(config) {
		this.config = config
		this.updateStatus(InstanceStatus.Connecting)
		await this.initConnection()
	}

	async destroy() {
		if (this.pollTimer) {
			clearInterval(this.pollTimer)
			this.pollTimer = null
		}
	}

	async configUpdated(config) {
		this.config = config
		await this.initConnection()
	}

	getConfigFields() {
		return [
			{
				type: 'textinput',
				id: 'host',
				label: 'Dante Streamer Host / IP',
				width: 8,
				default: '127.0.0.1',
			},
			{
				type: 'number',
				id: 'port',
				label: 'HTTP Port',
				width: 4,
				default: 8085,
				min: 1,
				max: 65535,
			},
			{
				type: 'number',
				id: 'default_zone',
				label: 'Default Zone ID',
				width: 6,
				default: 1,
				min: 1,
				max: 8,
			},
			{
				type: 'number',
				id: 'poll_interval',
				label: 'Polling Interval (seconds)',
				width: 6,
				default: 2,
				min: 1,
				max: 60,
			},
		]
	}

	async initConnection() {
		if (this.pollTimer) {
			clearInterval(this.pollTimer)
			this.pollTimer = null
		}

		const host = this.config.host || '127.0.0.1'
		const port = this.config.port || 8085
		this.baseUrl = `http://${host}:${port}`

		// Initial fetch
		await this.pollStatus()

		// Start polling loop
		const intervalMs = Math.max(1, this.config.poll_interval || 2) * 1000
		this.pollTimer = setInterval(() => {
			this.pollStatus()
		}, intervalMs)
	}

	async request(endpoint, method = 'GET', body = null) {
		try {
			const options = {
				method,
				headers: {
					'Content-Type': 'application/json',
				},
			}
			if (body) {
				options.body = JSON.stringify(body)
			}
			const res = await fetch(`${this.baseUrl}${endpoint}`, options)
			if (!res.ok) {
				throw new Error(`HTTP ${res.status}: ${res.statusText}`)
			}
			return await res.json()
		} catch (err) {
			this.log('error', `Request to ${endpoint} failed: ${err.message}`)
			throw err
		}
	}

	async pollStatus() {
		try {
			const status = await this.request('/api/status')
			this.updateStatus(InstanceStatus.Ok)

			// Check if sound list or zone list has changed
			const newSounds = (status.soundboard && status.soundboard.sounds) || []
			const newZones = status.zones || []

			const soundsChanged = JSON.stringify(this.sounds) !== JSON.stringify(newSounds)
			const zonesChanged = JSON.stringify(this.zones) !== JSON.stringify(newZones)

			this.sounds = newSounds
			this.zones = newZones
			this.soundboardState = status.soundboard || { enabled: false, sounds: [], playing: [] }

			if (soundsChanged || zonesChanged) {
				this.updateActions()
				this.updateFeedbacks()
				this.updateVariableDefinitions()
				this.updatePresets()
			}

			this.updateVariableValues(status)
			this.checkFeedbacks()
		} catch (err) {
			this.updateStatus(InstanceStatus.ConnectionFailure, err.message)
		}
	}

	getZoneChoices() {
		if (!this.zones || this.zones.length === 0) {
			return [{ id: 1, label: 'Zone 1' }]
		}
		return this.zones.map((z) => ({
			id: z.id,
			label: z.name ? `${z.name} (Zone ${z.id})` : `Zone ${z.id}`,
		}))
	}

	getSoundChoices() {
		if (!this.sounds || this.sounds.length === 0) {
			return [{ id: '', label: '(No sounds found in soundboard)' }]
		}
		return this.sounds.map((s) => ({
			id: s.id,
			label: s.duration_ms ? `${s.name} (${(s.duration_ms / 1000).toFixed(1)}s)` : s.name,
		}))
	}

	updateActions() {
		const soundChoices = this.getSoundChoices()
		const zoneChoices = this.getZoneChoices()
		const defaultZone = this.config.default_zone || 1

		this.setActionDefinitions({
			play_sound: {
				name: 'Play Soundboard Sound',
				options: [
					{
						type: 'dropdown',
						id: 'sound_id',
						label: 'Sound',
						default: soundChoices[0]?.id || '',
						choices: soundChoices,
					},
					{
						type: 'dropdown',
						id: 'zone_id',
						label: 'Target Zone',
						default: defaultZone,
						choices: zoneChoices,
					},
				],
				callback: async (action) => {
					const soundId = action.options.sound_id
					const zoneId = Number(action.options.zone_id) || defaultZone
					if (!soundId) return
					await this.request('/api/soundboard/play', 'POST', {
						sound_id: soundId,
						zone_id: zoneId,
					})
					await this.pollStatus()
				},
			},

			play_sound_by_id: {
				name: 'Play Sound by Filename / ID (Custom / Expression)',
				options: [
					{
						type: 'textinput',
						id: 'sound_id',
						label: 'Sound ID (e.g. airhorn.mp3)',
						default: 'airhorn.mp3',
						useVariables: true,
					},
					{
						type: 'dropdown',
						id: 'zone_id',
						label: 'Target Zone',
						default: defaultZone,
						choices: zoneChoices,
					},
				],
				callback: async (action, context) => {
					const soundId = await context.parseVariablesInString(action.options.sound_id)
					const zoneId = Number(action.options.zone_id) || defaultZone
					if (!soundId) return
					await this.request('/api/soundboard/play', 'POST', {
						sound_id: soundId,
						zone_id: zoneId,
					})
					await this.pollStatus()
				},
			},

			stop_all_sounds: {
				name: 'Stop All Sounds',
				options: [
					{
						type: 'dropdown',
						id: 'zone_id',
						label: 'Target Zone',
						default: 0,
						choices: [{ id: 0, label: 'All Zones' }, ...zoneChoices],
					},
				],
				callback: async (action) => {
					const zoneId = Number(action.options.zone_id) || 0
					await this.request('/api/soundboard/stop-all', 'POST', {
						zone_id: zoneId,
					})
					await this.pollStatus()
				},
			},

			stop_voice: {
				name: 'Stop Specific Sound Voice ID',
				options: [
					{
						type: 'number',
						id: 'voice_id',
						label: 'Voice ID',
						default: 1,
						min: 1,
						max: 99999,
					},
				],
				callback: async (action) => {
					const voiceId = Number(action.options.voice_id)
					if (!voiceId) return
					await this.request('/api/soundboard/stop', 'POST', {
						voice_id: voiceId,
					})
					await this.pollStatus()
				},
			},

			toggle_zone_mute: {
				name: 'Toggle Zone Mute',
				options: [
					{
						type: 'dropdown',
						id: 'zone_id',
						label: 'Zone',
						default: defaultZone,
						choices: zoneChoices,
					},
				],
				callback: async (action) => {
					const zoneId = Number(action.options.zone_id) || defaultZone
					await this.request(`/api/zones/${zoneId}/mute`, 'POST')
					await this.pollStatus()
				},
			},

			set_zone_volume: {
				name: 'Set Zone Volume',
				options: [
					{
						type: 'dropdown',
						id: 'zone_id',
						label: 'Zone',
						default: defaultZone,
						choices: zoneChoices,
					},
					{
						type: 'number',
						id: 'volume',
						label: 'Volume (0 - 100)',
						default: 80,
						min: 0,
						max: 100,
					},
				],
				callback: async (action) => {
					const zoneId = Number(action.options.zone_id) || defaultZone
					const volume = Number(action.options.volume)
					await this.request(`/api/zones/${zoneId}/volume`, 'POST', { volume })
					await this.pollStatus()
				},
			},
		})
	}

	updateFeedbacks() {
		const soundChoices = this.getSoundChoices()
		const zoneChoices = [{ id: 0, label: 'Any Zone' }, ...this.getZoneChoices()]

		this.setFeedbackDefinitions({
			sound_playing: {
				type: 'boolean',
				name: 'Sound is Actively Playing',
				description: 'Highlights the button when a specific sound is currently sounding',
				defaultStyle: {
					bgcolor: combineRgb(255, 68, 68),
					color: combineRgb(255, 255, 255),
				},
				options: [
					{
						type: 'dropdown',
						id: 'sound_id',
						label: 'Sound',
						default: soundChoices[0]?.id || '',
						choices: soundChoices,
					},
					{
						type: 'dropdown',
						id: 'zone_id',
						label: 'Zone',
						default: 0,
						choices: zoneChoices,
					},
				],
				callback: (feedback) => {
					const soundId = feedback.options.sound_id
					const zoneId = Number(feedback.options.zone_id) || 0
					const playing = (this.soundboardState && this.soundboardState.playing) || []

					return playing.some((p) => {
						const matchSound = p.sound_id === soundId
						const matchZone = zoneId === 0 || p.zone_id === zoneId
						return matchSound && matchZone
					})
				},
			},

			any_sound_playing: {
				type: 'boolean',
				name: 'Any Soundboard Sound Playing on Zone',
				description: 'Highlights when any sound is currently active on the target zone',
				defaultStyle: {
					bgcolor: combineRgb(255, 170, 0),
					color: combineRgb(0, 0, 0),
				},
				options: [
					{
						type: 'dropdown',
						id: 'zone_id',
						label: 'Zone',
						default: 0,
						choices: zoneChoices,
					},
				],
				callback: (feedback) => {
					const zoneId = Number(feedback.options.zone_id) || 0
					const playing = (this.soundboardState && this.soundboardState.playing) || []
					if (zoneId === 0) return playing.length > 0
					return playing.some((p) => p.zone_id === zoneId)
				},
			},

			zone_muted: {
				type: 'boolean',
				name: 'Zone is Muted',
				description: 'Highlights when the selected zone is muted',
				defaultStyle: {
					bgcolor: combineRgb(200, 0, 0),
					color: combineRgb(255, 255, 255),
				},
				options: [
					{
						type: 'dropdown',
						id: 'zone_id',
						label: 'Zone',
						default: this.config.default_zone || 1,
						choices: this.getZoneChoices(),
					},
				],
				callback: (feedback) => {
					const zoneId = Number(feedback.options.zone_id)
					const zone = (this.zones || []).find((z) => z.id === zoneId)
					return !!(zone && zone.muted)
				},
			},
		})
	}

	updateVariableDefinitions() {
		const variables = [
			{ variableId: 'soundboard_enabled', name: 'Soundboard Enabled' },
			{ variableId: 'sounds_count', name: 'Total Sounds Count' },
			{ variableId: 'active_voices_count', name: 'Active Voices Count' },
		]

		// Dynamic variables for each zone
		for (const z of this.zones || []) {
			variables.push(
				{ variableId: `zone_${z.id}_name`, name: `Zone ${z.id} Name` },
				{ variableId: `zone_${z.id}_volume`, name: `Zone ${z.id} Volume` },
				{ variableId: `zone_${z.id}_muted`, name: `Zone ${z.id} Muted` },
				{ variableId: `zone_${z.id}_playing_sound`, name: `Zone ${z.id} Currently Playing Sound` }
			)
		}

		// Dynamic variables for indexed sounds
		for (let i = 0; i < (this.sounds || []).length; i++) {
			const num = i + 1
			variables.push(
				{ variableId: `sound_${num}_id`, name: `Sound ${num} ID` },
				{ variableId: `sound_${num}_name`, name: `Sound ${num} Name` },
				{ variableId: `sound_${num}_duration_s`, name: `Sound ${num} Duration (seconds)` }
			)
		}

		this.setVariableDefinitions(variables)
	}

	updateVariableValues(status) {
		const playing = (status.soundboard && status.soundboard.playing) || []
		const sounds = (status.soundboard && status.soundboard.sounds) || []

		const values = {
			soundboard_enabled: !!(status.soundboard && status.soundboard.enabled),
			sounds_count: sounds.length,
			active_voices_count: playing.length,
		}

		// Update zone variables
		for (const z of status.zones || []) {
			values[`zone_${z.id}_name`] = z.name || `Zone ${z.id}`
			values[`zone_${z.id}_volume`] = z.volume ?? 80
			values[`zone_${z.id}_muted`] = z.muted ? 'true' : 'false'

			const activeVoice = playing.find((p) => p.zone_id === z.id)
			values[`zone_${z.id}_playing_sound`] = activeVoice ? activeVoice.name : 'None'
		}

		// Update indexed sound variables
		for (let i = 0; i < sounds.length; i++) {
			const num = i + 1
			const s = sounds[i]
			values[`sound_${num}_id`] = s.id
			values[`sound_${num}_name`] = s.name
			values[`sound_${num}_duration_s`] = s.duration_ms ? (s.duration_ms / 1000).toFixed(1) : '0'
		}

		this.setVariableValues(values)
	}

	updatePresets() {
		const presets = {}
		const defaultZone = this.config.default_zone || 1

		// 1. Dynamic Presets for every sound found in the soundboard
		for (const sound of this.sounds || []) {
			const durationStr = sound.duration_ms ? `\n${(sound.duration_ms / 1000).toFixed(1)}s` : ''
			presets[`sound_pad_${sound.id}`] = {
				type: 'button',
				category: 'Soundboard Pads',
				name: `Play ${sound.name}`,
				style: {
					text: `${sound.name}${durationStr}`,
					size: 'auto',
					color: combineRgb(255, 255, 255),
					bgcolor: combineRgb(30, 30, 40),
				},
				steps: [
					{
						down: [
							{
								actionId: 'play_sound',
								options: {
									sound_id: sound.id,
									zone_id: defaultZone,
								},
							},
						],
						up: [],
					},
				],
				feedbacks: [
					{
						feedbackId: 'sound_playing',
						options: {
							sound_id: sound.id,
							zone_id: 0,
						},
						style: {
							bgcolor: combineRgb(255, 68, 68),
							color: combineRgb(255, 255, 255),
						},
					},
				],
			}
		}

		// 2. Global Soundboard Control Presets
		presets['stop_all_sounds'] = {
			type: 'button',
			category: 'Soundboard Controls',
			name: 'Stop All Sounds',
			style: {
				text: 'STOP ALL\nSOUNDS',
				size: '14',
				color: combineRgb(255, 255, 255),
				bgcolor: combineRgb(180, 0, 0),
			},
			steps: [
				{
					down: [
						{
							actionId: 'stop_all_sounds',
							options: {
								zone_id: 0,
							},
						},
					],
					up: [],
				},
			],
			feedbacks: [
				{
					feedbackId: 'any_sound_playing',
					options: {
						zone_id: 0,
					},
					style: {
						bgcolor: combineRgb(255, 0, 0),
					},
				},
			],
		}

		// 3. Zone Control Presets
		for (const z of this.zones || []) {
			presets[`zone_${z.id}_mute`] = {
				type: 'button',
				category: 'Zone Controls',
				name: `Mute ${z.name || `Zone ${z.id}`}`,
				style: {
					text: `${z.name || `Zone ${z.id}`}\nMUTE`,
					size: '14',
					color: combineRgb(255, 255, 255),
					bgcolor: combineRgb(50, 50, 60),
				},
				steps: [
					{
						down: [
							{
								actionId: 'toggle_zone_mute',
								options: {
									zone_id: z.id,
								},
							},
						],
						up: [],
					},
				],
				feedbacks: [
					{
						feedbackId: 'zone_muted',
						options: {
							zone_id: z.id,
						},
						style: {
							bgcolor: combineRgb(255, 0, 0),
							color: combineRgb(255, 255, 255),
						},
					},
				],
			}
		}

		this.setPresetDefinitions(presets)
	}
}

runEntrypoint(DanteStreamerInstance, [])
