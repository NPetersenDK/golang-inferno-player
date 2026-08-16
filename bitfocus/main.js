import { InstanceBase, InstanceStatus, combineRgb } from '@companion-module/base'

export default class DanteStreamerInstance extends InstanceBase {
	constructor(internal) {
		super(internal)
		this.sounds = []
		this.zones = []
		this.stations = []
		this.tuner = { enabled: false, presets: [] }
		this.soundboardState = { enabled: false, sounds: [], playing: [] }
		this.pollTimer = null
	}

	async init(config) {
		this.config = config
		this.updateStatus(InstanceStatus.Connecting)
		this.updateActions()
		this.updateFeedbacks()
		this.updateVariableDefinitions()
		this.updatePresets()
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
	}

	async pollStatus() {
		try {
			const [status, stations] = await Promise.all([
				this.request('/api/status'),
				this.request('/api/presets').catch(() => []),
			])
			this.updateStatus(InstanceStatus.Ok)

			const newSounds = (status.soundboard && status.soundboard.sounds) || []
			const newZones = status.zones || []
			const newTuner = status.tuner || { enabled: false, presets: [] }
			const newStations = Array.isArray(stations) ? stations : []

			const soundsChanged = JSON.stringify(this.sounds) !== JSON.stringify(newSounds)
			const zonesChanged = JSON.stringify(this.zones) !== JSON.stringify(newZones)
			const tunerChanged = JSON.stringify(this.tuner) !== JSON.stringify(newTuner)
			const stationsChanged = JSON.stringify(this.stations) !== JSON.stringify(newStations)

			this.sounds = newSounds
			this.zones = newZones
			this.tuner = newTuner
			this.stations = newStations
			this.soundboardState = status.soundboard || { enabled: false, sounds: [], playing: [] }

			if (soundsChanged || zonesChanged || tunerChanged || stationsChanged) {
				this.updateActions()
				this.updateFeedbacks()
				this.updateVariableDefinitions()
				this.updatePresets()
			}

			this.updateVariableValues(status)
			this.checkAllFeedbacks()
		} catch (err) {
			this.updateStatus(InstanceStatus.ConnectionFailure, err.message)
		}
	}

	getZoneChoices(onlyPlayable = false) {
		if (!this.zones || this.zones.length === 0) {
			return [{ id: 1, label: 'Zone 1' }]
		}
		const filtered = onlyPlayable ? this.zones.filter((z) => !z.is_source) : this.zones
		return (filtered.length > 0 ? filtered : this.zones).map((z) => ({
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

	getStationChoices() {
		if (!this.stations || this.stations.length === 0) {
			return [{ id: '', label: '(No stations configured)' }]
		}
		return this.stations.map((s) => ({
			id: s.id,
			label: s.category ? `${s.name} (${s.category})` : s.name,
		}))
	}

	getTunerChoices() {
		const presets = (this.tuner && this.tuner.presets) || []
		if (presets.length === 0) {
			return [{ id: '', label: '(No FM presets configured)' }]
		}
		return presets.map((p) => ({
			id: p.id,
			label: p.frequency_hz ? `${p.name} (${(p.frequency_hz / 1e6).toFixed(1)} MHz)` : p.name,
		}))
	}

	updateActions() {
		const soundChoices = this.getSoundChoices()
		const zoneChoices = this.getZoneChoices()
		const playableZoneChoices = this.getZoneChoices(true)
		const stationChoices = this.getStationChoices()
		const tunerChoices = this.getTunerChoices()
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

			play_station: {
				name: 'Play Internet Radio Station',
				options: [
					{
						type: 'dropdown',
						id: 'zone_id',
						label: 'Target Zone',
						default: playableZoneChoices[0]?.id || 1,
						choices: playableZoneChoices,
					},
					{
						type: 'dropdown',
						id: 'preset_id',
						label: 'Station Preset',
						default: stationChoices[0]?.id || '',
						choices: stationChoices,
					},
				],
				callback: async (action) => {
					const zoneId = Number(action.options.zone_id) || 1
					const presetId = action.options.preset_id
					if (!presetId) return

					const zone = (this.zones || []).find((z) => z.id === zoneId)
					const isCurrentlyPlayingThis =
						zone &&
						(zone.status === 'playing' || zone.status === 'buffering') &&
						(zone.station_id === presetId || zone.station_name === presetId)

					if (isCurrentlyPlayingThis) {
						await this.request(`/api/zones/${zoneId}/stop`, 'POST')
					} else {
						await this.request(`/api/zones/${zoneId}/play`, 'POST', { preset_id: presetId })
					}
					await this.pollStatus()
				},
			},

			stop_zone: {
				name: 'Stop Zone Playback',
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
					await this.request(`/api/zones/${zoneId}/stop`, 'POST')
					await this.pollStatus()
				},
			},

			tune_fm: {
				name: 'Tune FM Radio Preset',
				options: [
					{
						type: 'dropdown',
						id: 'preset_id',
						label: 'FM Preset',
						default: tunerChoices[0]?.id || '',
						choices: tunerChoices,
					},
				],
				callback: async (action) => {
					const presetId = action.options.preset_id
					if (!presetId) return

					const isCurrentlyTunedThis =
						this.tuner && this.tuner.tuned && this.tuner.preset_id === presetId

					if (isCurrentlyTunedThis) {
						await this.request('/api/tuner/off', 'POST')
					} else {
						await this.request('/api/tuner/tune', 'POST', { preset_id: presetId })
					}
					await this.pollStatus()
				},
			},

			tuner_off: {
				name: 'Turn FM Radio Tuner Off',
				options: [],
				callback: async () => {
					await this.request('/api/tuner/off', 'POST')
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
		const playableZoneChoices = this.getZoneChoices(true)
		const stationChoices = this.getStationChoices()
		const tunerChoices = this.getTunerChoices()

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

			station_playing: {
				type: 'boolean',
				name: 'Radio Station is Playing on Zone',
				description: 'Highlights when the selected radio station is active on the zone',
				defaultStyle: {
					bgcolor: combineRgb(0, 180, 50),
					color: combineRgb(255, 255, 255),
				},
				options: [
					{
						type: 'dropdown',
						id: 'zone_id',
						label: 'Zone',
						default: playableZoneChoices[0]?.id || 1,
						choices: playableZoneChoices,
					},
					{
						type: 'dropdown',
						id: 'preset_id',
						label: 'Station',
						default: stationChoices[0]?.id || '',
						choices: stationChoices,
					},
				],
				callback: (feedback) => {
					const zoneId = Number(feedback.options.zone_id)
					const presetId = feedback.options.preset_id
					const zone = (this.zones || []).find((z) => z.id === zoneId)
					if (!zone) return false
					const isPlaying = zone.status === 'playing' || zone.status === 'buffering'
					return isPlaying && (zone.station_id === presetId || zone.station_name === presetId)
				},
			},

			fm_tuned: {
				type: 'boolean',
				name: 'FM Radio is Tuned to Preset',
				description: 'Highlights when FM radio tuner is tuned to the preset',
				defaultStyle: {
					bgcolor: combineRgb(0, 160, 200),
					color: combineRgb(255, 255, 255),
				},
				options: [
					{
						type: 'dropdown',
						id: 'preset_id',
						label: 'FM Preset',
						default: tunerChoices[0]?.id || '',
						choices: tunerChoices,
					},
				],
				callback: (feedback) => {
					const presetId = feedback.options.preset_id
					return !!(this.tuner && this.tuner.tuned && this.tuner.preset_id === presetId)
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

			zone_volume_equals: {
				type: 'boolean',
				name: 'Zone Volume Equals Value',
				description: 'Highlights when the zone volume matches the level (e.g. 0, 25, 50, 75, 100)',
				defaultStyle: {
					bgcolor: combineRgb(40, 120, 220),
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
					{
						type: 'number',
						id: 'volume',
						label: 'Volume Level',
						default: 50,
						min: 0,
						max: 100,
					},
				],
				callback: (feedback) => {
					const zoneId = Number(feedback.options.zone_id)
					const targetVolume = Number(feedback.options.volume)
					const zone = (this.zones || []).find((z) => z.id === zoneId)
					return !!(zone && zone.volume === targetVolume)
				},
			},

			zone_playing: {
				type: 'boolean',
				name: 'Zone is Actively Playing Audio',
				description: 'Highlights when the zone is playing any stream or source',
				defaultStyle: {
					bgcolor: combineRgb(200, 60, 60),
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
					return !!(zone && (zone.status === 'playing' || zone.status === 'buffering'))
				},
			},
		})
	}

	updateVariableDefinitions() {
		const variables = {
			soundboard_enabled: { name: 'Soundboard Enabled' },
			sounds_count: { name: 'Total Sounds Count' },
			active_voices_count: { name: 'Active Voices Count' },
			fm_tuner_tuned: { name: 'FM Tuner Tuned State' },
			fm_tuner_station: { name: 'FM Tuner Active Station' },
		}

		// Dynamic variables for each zone
		for (const z of this.zones || []) {
			variables[`zone_${z.id}_name`] = { name: `Zone ${z.id} Name` }
			variables[`zone_${z.id}_volume`] = { name: `Zone ${z.id} Volume` }
			variables[`zone_${z.id}_muted`] = { name: `Zone ${z.id} Muted` }
			variables[`zone_${z.id}_status`] = { name: `Zone ${z.id} Playback Status` }
			variables[`zone_${z.id}_station`] = { name: `Zone ${z.id} Current Station` }
			variables[`zone_${z.id}_playing_sound`] = { name: `Zone ${z.id} Currently Playing Sound` }
		}

		// Dynamic variables for indexed sounds
		for (let i = 0; i < (this.sounds || []).length; i++) {
			const num = i + 1
			variables[`sound_${num}_id`] = { name: `Sound ${num} ID` }
			variables[`sound_${num}_name`] = { name: `Sound ${num} Name` }
			variables[`sound_${num}_duration_s`] = { name: `Sound ${num} Duration (seconds)` }
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
			fm_tuner_tuned: !!(status.tuner && status.tuner.tuned),
			fm_tuner_station: (status.tuner && status.tuner.station_name) || (status.tuner && status.tuner.preset_id) || 'Off',
		}

		// Update zone variables
		for (const z of status.zones || []) {
			values[`zone_${z.id}_name`] = z.name || `Zone ${z.id}`
			values[`zone_${z.id}_volume`] = z.volume ?? 80
			values[`zone_${z.id}_muted`] = z.muted ? 'true' : 'false'
			values[`zone_${z.id}_status`] = z.status || 'idle'
			values[`zone_${z.id}_station`] = z.station_name || z.current_title || 'None'

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
		const soundPresetIds = []
		const radioPresetIds = []
		const fmPresetIds = []
		const zoneControlPresetIds = []
		const defaultZone = this.config.default_zone || 1

		// 1. Soundboard Pads Presets (e.g. Lydnavn (3.5s))
		for (const sound of this.sounds || []) {
			const presetId = `sound_pad_${sound.id}`
			soundPresetIds.push(presetId)
			const durationStr = sound.duration_ms ? `\n(${(sound.duration_ms / 1000).toFixed(1)}s)` : ''
			presets[presetId] = {
				type: 'simple',
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
			type: 'simple',
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

		// 3. Internet Radio Stations Presets
		const playableZones = (this.zones || []).filter((z) => !z.is_source)
		for (const z of playableZones.length > 0 ? playableZones : [{ id: 1, name: 'Zone 1' }]) {
			for (const s of this.stations || []) {
				const presetId = `radio_${z.id}_${s.id}`
				radioPresetIds.push(presetId)
				presets[presetId] = {
					type: 'simple',
					name: `Play ${s.name} on ${z.name || `Zone ${z.id}`}`,
					style: {
						text: `${s.name}\n${z.name || `Zone ${z.id}`}`,
						size: 'auto',
						color: combineRgb(255, 255, 255),
						bgcolor: combineRgb(20, 50, 80),
					},
					steps: [
						{
							down: [
								{
									actionId: 'play_station',
									options: {
										zone_id: z.id,
										preset_id: s.id,
									},
								},
							],
							up: [],
						},
					],
					feedbacks: [
						{
							feedbackId: 'station_playing',
							options: {
								zone_id: z.id,
								preset_id: s.id,
							},
							style: {
								bgcolor: combineRgb(0, 180, 50),
								color: combineRgb(255, 255, 255),
							},
						},
					],
				}
			}
		}

		// 4. FM Radio Tuner Presets (if tuner enabled)
		if (this.tuner && this.tuner.enabled) {
			for (const p of this.tuner.presets || []) {
				const presetId = `fm_preset_${p.id}`
				fmPresetIds.push(presetId)
				const mhz = p.frequency_hz ? `\n${(p.frequency_hz / 1e6).toFixed(1)}M` : ''
				presets[presetId] = {
					type: 'simple',
					name: `Tune FM ${p.name}`,
					style: {
						text: `FM ${p.name}${mhz}`,
						size: 'auto',
						color: combineRgb(255, 255, 255),
						bgcolor: combineRgb(60, 40, 90),
					},
					steps: [
						{
							down: [
								{
									actionId: 'tune_fm',
									options: {
										preset_id: p.id,
									},
								},
							],
							up: [],
						},
					],
					feedbacks: [
						{
							feedbackId: 'fm_tuned',
							options: {
								preset_id: p.id,
							},
							style: {
								bgcolor: combineRgb(0, 160, 200),
								color: combineRgb(255, 255, 255),
							},
						},
					],
				}
			}

			presets['fm_tuner_off'] = {
				type: 'simple',
				name: 'Turn FM Radio Off',
				style: {
					text: 'FM RADIO\nOFF',
					size: '14',
					color: combineRgb(255, 255, 255),
					bgcolor: combineRgb(90, 30, 40),
				},
				steps: [
					{
						down: [
							{
								actionId: 'tuner_off',
								options: {},
							},
						],
						up: [],
					},
				],
				feedbacks: [],
			}
			fmPresetIds.push('fm_tuner_off')
		}

		// 5. Zone Control Presets (Mute, Volume 0%, 25%, 50%, 75%, 100%, and Stop)
		for (const z of this.zones || [{ id: 1, name: 'Zone 1' }]) {
			const zName = z.name || `Zone ${z.id}`

			// Mute button
			const muteId = `zone_${z.id}_mute`
			zoneControlPresetIds.push(muteId)
			presets[muteId] = {
				type: 'simple',
				name: `Mute ${zName}`,
				style: {
					text: `${zName}\nMUTE`,
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

			// Stop playback button
			const stopId = `zone_${z.id}_stop`
			zoneControlPresetIds.push(stopId)
			presets[stopId] = {
				type: 'simple',
				name: `Stop ${zName}`,
				style: {
					text: `${zName}\nSTOP`,
					size: '14',
					color: combineRgb(255, 255, 255),
					bgcolor: combineRgb(80, 40, 40),
				},
				steps: [
					{
						down: [
							{
								actionId: 'stop_zone',
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
						feedbackId: 'zone_playing',
						options: {
							zone_id: z.id,
						},
						style: {
							bgcolor: combineRgb(200, 60, 60),
							color: combineRgb(255, 255, 255),
						},
					},
				],
			}

			// Volume level presets: 0%, 25%, 50%, 75%, 100%
			const volumeLevels = [0, 25, 50, 75, 100]
			for (const vol of volumeLevels) {
				const volId = `zone_${z.id}_vol_${vol}`
				zoneControlPresetIds.push(volId)
				presets[volId] = {
					type: 'simple',
					name: `${zName} Volume ${vol}%`,
					style: {
						text: `${zName}\n${vol}%`,
						size: '14',
						color: combineRgb(255, 255, 255),
						bgcolor: combineRgb(35, 45, 55),
					},
					steps: [
						{
							down: [
								{
									actionId: 'set_zone_volume',
									options: {
										zone_id: z.id,
										volume: vol,
									},
								},
							],
							up: [],
						},
					],
					feedbacks: [
						{
							feedbackId: 'zone_volume_equals',
							options: {
								zone_id: z.id,
								volume: vol,
							},
							style: {
								bgcolor: combineRgb(0, 120, 220),
								color: combineRgb(255, 255, 255),
							},
						},
					],
				}
			}
		}

		// Structure definition for sections
		const structure = [
			{
				id: 'soundboard_pads',
				name: 'Soundboard Pads',
				definitions: soundPresetIds,
			},
			{
				id: 'soundboard_controls',
				name: 'Soundboard Controls',
				definitions: ['stop_all_sounds'],
			},
		]

		if (radioPresetIds.length > 0) {
			structure.push({
				id: 'internet_radio',
				name: 'Internet Radio Stations',
				definitions: radioPresetIds,
			})
		}

		if (fmPresetIds.length > 0) {
			structure.push({
				id: 'fm_radio',
				name: 'FM Radio Tuner',
				definitions: fmPresetIds,
			})
		}

		structure.push({
			id: 'zone_controls',
			name: 'Zone Controls (Volume, Mute & Stop)',
			definitions: zoneControlPresetIds,
		})

		this.setPresetDefinitions(structure, presets)
	}
}
