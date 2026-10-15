package main

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// fakeSource is a small Audiobookshelf checkout in the shapes the scan reads: a router built by the server with the server's managers, a
// controller with middleware, a manager that answers for a handler, two records linked one to many, and routes written in place.
var fakeSource = fstest.MapFS{
	"package.json": {Data: []byte(`{"name": "audiobookshelf", "version": "9.9.9"}`)},

	"server/Server.js": {Data: []byte(`
const ApiRouter = require('./routers/ApiRouter')
const PlaybackManager = require('./managers/PlaybackManager')

class Server {
  constructor() {
    this.playbackManager = new PlaybackManager()
    this.apiRouter = new ApiRouter(this)
  }

  start(router) {
    router.get('/status', (req, res) => {
      res.json({ isInit: true, version: '9.9.9' })
    })
    router.get('/healthcheck', (req, res) => res.sendStatus(200))
  }
}
module.exports = Server
`)},

	"server/routers/ApiRouter.js": {Data: []byte(`
const express = require('express')
const LibraryController = require('../controllers/LibraryController')
const ItemController = require('../controllers/ItemController')

class ApiRouter {
  constructor(Server) {
    this.playbackManager = Server.playbackManager
    this.router = express()
    this.init()
  }

  init() {
    this.router.get('/libraries', LibraryController.findAll.bind(this))
    this.router.get('/libraries/:id', LibraryController.middleware.bind(this), LibraryController.findOne.bind(this))
    this.router.patch('/libraries/:id', LibraryController.middleware.bind(this), LibraryController.update.bind(this))
    this.router.post('/libraries/:id/cover', LibraryController.middleware.bind(this), LibraryController.uploadCover.bind(this))
    this.router.get('/libraries/:id/raw', LibraryController.middleware.bind(this), LibraryController.raw.bind(this))
    this.router.get('/libraries/:id/label', LibraryController.middleware.bind(this), LibraryController.label.bind(this))
    this.router.get('/libraries/:id/search', LibraryController.middleware.bind(this), LibraryController.search.bind(this))
    this.router.post('/libraries/:id/notes', LibraryController.middleware.bind(this), LibraryController.notes.bind(this))
    this.router.post('/libraries/order', LibraryController.order.bind(this))
    this.router.get('/items/:id/play/:episodeId?', ItemController.play.bind(this))
    this.router.get('/items/:id/download', ItemController.download.bind(this))
    this.router.get('/files/*', ItemController.download.bind(this))
  }
}
module.exports = ApiRouter
`)},

	"server/managers/PlaybackManager.js": {Data: []byte(`
class PlaybackManager {
  start(req, res, episodeId) {
    if (!req.query.force) return res.status(400).send('not forced')
    res.json({ episodeId, started: Date.now() })
  }
}
module.exports = PlaybackManager
`)},

	"server/controllers/LibraryController.js": {Data: []byte(`
const Database = require('../Database')

class LibraryController {
  constructor() {}

  async middleware(req, res, next) {
    req.library = await Database.libraryModel.findByPk(req.params.id)
    if (!req.library) return res.sendStatus(404)
    if (!req.user.isAdmin) return res.sendStatus(403)
    next()
  }

  async findAll(req, res) {
    const { limit, page } = req.query
    const libraries = await Database.libraryModel.findAll()
    const payload = { results: libraries.map((l) => l.toOldJSON()), total: libraries.length }
    payload.limit = Number(limit) || 0
    res.json(payload)
  }

  findOne(req, res) {
    const out = req.library.toOldJSON()
    out.extra = 'yes'
    res.json(out)
  }

  async update(req, res) {
    await req.library.updateFromRequest(req.body)
    res.sendStatus(200)
  }

  async uploadCover(req, res) {
    if (!req.files?.cover) return res.status(400).send('no cover')
    if (req.library.isPublic) return res.status(409).send('public')
    try {
      switch (req.query.kind) {
        case 'jpg':
          break
        default:
          while (req.query.wait) {
            await req.library.reload()
          }
      }
    } catch (error) {
      return res.status(500).send(error.message)
    } finally {
      req.library.changed('settings', true)
    }
    if (req.query.back) return res.redirect('/library')
    res.status(201).end()
  }

  raw(req, res) {
    res.json(req.library)
  }

  label(req, res) {
    const who = req.query.who ? req.user : req.library
    res.json({ label: who.label() })
  }

  notes(req, res) {
    const payload = req.body
    if (!payload.text) return res.sendStatus(400)
    const { mediaItemType } = req.body
    if (!['book', 'podcastEpisode'].includes(mediaItemType)) return res.sendStatus(400)
    for (const key in req.body) {
      if (key.startsWith('_')) return res.sendStatus(400)
    }
    res.sendStatus(204)
  }

  order(req, res) {
    const orders = req.body
    if (!orders?.length) return res.sendStatus(400)
    orders.forEach((o) => o.id)
    res.sendStatus(200)
  }

  search(req, res) {
    const page = toNumber(req.query.page, 0)
    const limit = req.query.limit || 10
    const near = parseFloat(req.query.near)
    const kind = getQueryParamAsString(req.query, 'kind', 'book')
    const sorts = ['displayTitle', 'timeListening']
    let sort = req.query.sort || 'displayTitle'
    if (!sorts.includes(sort)) sort = 'displayTitle'
    const found = searchIndex(req.query.q, page, limit, near, kind, sort)
    const stringKey = found.length ? 'MessageInvalidAsin' : 'MessageChaptersNotFound'
    res.json({ results: found, total: found.length, page, limit, stringKey, note: 'One note' })
  }
}
module.exports = new LibraryController()
`)},

	"server/controllers/ItemController.js": {Data: []byte(`
class ItemController {
  play(req, res) {
    return this.playbackManager.start(req, res, req.params.episodeId)
  }

  download(req, res) {
    res.download('/tmp/book.m4b')
  }
}
module.exports = new ItemController()
`)},

	"server/Database.js": {Data: []byte(`
class Database {
  get models() {
    return this.sequelize.models
  }

  get libraryModel() {
    return this.models.library
  }
}
module.exports = new Database()
`)},

	"server/models/Library.js": {Data: []byte(`
const { DataTypes, Model } = require('sequelize')

class Library extends Model {
  static init(sequelize) {
    super.init(
      {
        id: { type: DataTypes.UUID, primaryKey: true },
        name: DataTypes.STRING,
        displayOrder: { type: DataTypes.INTEGER, allowNull: false },
        settings: DataTypes.JSON,
        isPublic: DataTypes.BOOLEAN,
        lastScan: DataTypes.DATE
      },
      { sequelize, modelName: 'library' }
    )
  }

  static associate() {
    const { folder } = this.sequelize.models
    Library.hasMany(folder)
    Library.hasOne(folder, { as: 'mainFolder', foreignKey: { name: 'mainLibraryId' } })
    folder.belongsTo(Library)
  }

  toOldJSON() {
    return {
      id: this.id,
      name: this.name,
      displayOrder: this.displayOrder,
      folders: this.folders.map((f) => f.toOldJSON()),
      lastScan: this.lastScan?.valueOf() || null,
      settings: this.settings
    }
  }

  label() {
    return this.name || 'library'
  }

  async updateFromRequest(payload) {
    if (payload.name) this.name = payload.name
    if (payload.displayOrder !== undefined) this.displayOrder = payload.displayOrder
  }
}
module.exports = Library
`)},

	"server/models/Folder.js": {Data: []byte(`
const { DataTypes, Model } = require('sequelize')

class Folder extends Model {
  static init(sequelize) {
    super.init({ id: { type: DataTypes.UUID, primaryKey: true }, path: DataTypes.STRING }, { sequelize, modelName: 'folder', timestamps: false })
  }

  toOldJSON() {
    return { id: this.id, fullPath: this.path, libraryId: this.libraryId }
  }
}
module.exports = Folder
`)},

	"server/models/User.js": {Data: []byte(`
const { DataTypes, Model } = require('sequelize')

class User extends Model {
  static init(sequelize) {
    super.init({ id: { type: DataTypes.UUID, primaryKey: true }, type: DataTypes.STRING }, { sequelize, modelName: 'user' })
  }

  get isAdmin() {
    return this.type === 'admin'
  }

  label() {
    return 'user ' + this.id
  }
}
module.exports = User
`)},
}

// fakeProgram is fakeSource parsed, with the evaluator that scans it.
func fakeProgram(t *testing.T) *evaluator {
	t.Helper()

	prog, err := load(fakeSource, "server")
	if err != nil {
		t.Fatal(err)
	}

	return newEvaluator(prog, readModels(prog))
}

// writeFakeSource writes fakeSource into a folder, for the command that reads a checkout from disk.
func writeFakeSource(t *testing.T) string {
	t.Helper()

	return writeFS(t, fakeSource)
}

// fakeClient is a hand-written client for fakeSource, in the shapes byhand reads: an answer read into a named struct, a body written as a
// map, a value it types otherwise than the source, and a request to a route the source does not have.
var fakeClient = fstest.MapFS{
	"client.go": {Data: []byte(`package abs

import (
	"context"
	"net/url"
)

type Client struct{}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error { return nil }

func (c *Client) patch(ctx context.Context, path string, body, out any) error { return nil }
`)},

	"library.go": {Data: []byte(`package abs

import (
	"context"
	"encoding/json"
	"net/url"
)

type Library struct {
	ID       string          ` + "`json:\"id\"`" + `
	Settings LibrarySettings ` + "`json:\"settings\"`" + `
	Raw      json.RawMessage ` + "`json:\"raw\"`" + `
	Hidden   string          ` + "`json:\"-\"`" + `
}

type LibrarySettings struct {
	CoverAspectRatio int     ` + "`json:\"coverAspectRatio\"`" + `
	SkipMatching     *bool   ` + "`json:\"skipMatchingMediaWithAsin\"`" + `
	Ratio            float64
}

type Status struct {
	IsInit int ` + "`json:\"isInit\"`" + `
}

func (c *Client) Library(ctx context.Context, id string) (*Library, error) {
	var l Library
	if err := c.get(ctx, "/api/libraries/"+url.PathEscape(id), nil, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

func (c *Client) UpdateLibrary(ctx context.Context, id, name string) error {
	return c.patch(ctx, "/api/libraries/"+url.PathEscape(id), map[string]any{"name": name, "displayOrder": 2}, nil)
}

func (c *Client) Status(ctx context.Context) (*Status, error) {
	var s Status
	path := "/status"
	return &s, c.get(ctx, path, nil, &s)
}

func (c *Client) Nothing(ctx context.Context) error {
	return c.get(ctx, "/api/nothing", nil, nil)
}
`)},
}

// writeFS writes a set of files into a new folder.
func writeFS(t *testing.T, files fstest.MapFS) string {
	t.Helper()

	dir := t.TempDir()
	for name, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.Data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}
