module.exports = function rawLoader(source) {
  return `export default ${JSON.stringify(source)}`
}
