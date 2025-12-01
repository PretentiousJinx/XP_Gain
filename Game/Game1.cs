using Microsoft.Xna.Framework;
using Microsoft.Xna.Framework.Graphics;

namespace XP_Gain.Game
{
    public class Game1 : Game
    {
        private GraphicsDeviceManager _graphics;
        private SpriteBatch _spriteBatch;

        public Game1()
        {
            _graphics = new GraphicsDeviceManager(this);
            Content.RootDirectory = "Content";
        }

        protected override void LoadContent()
        {
            _spriteBatch = new SpriteBatch(GraphicsDevice);
            // Load hero sprite here
        }

        protected override void Update(GameTime gameTime)
        {
            // Update hero stats, XP, etc.
            base.Update(gameTime);
        }

        protected override void Draw(GameTime gameTime)
        {
            GraphicsDevice.Clear(Color.CornflowerBlue);
            _spriteBatch.Begin();
            // Draw hero here
            _spriteBatch.End();
            base.Draw(gameTime);
        }
    }
}
